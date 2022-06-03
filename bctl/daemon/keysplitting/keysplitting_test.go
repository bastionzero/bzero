package keysplitting

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"testing"
	"time"

	"bastionzero.com/bctl/v1/bctl/daemon/keysplitting/mocks"
	"bastionzero.com/bctl/v1/bctl/daemon/keysplitting/tokenrefresh"
	rrr "bastionzero.com/bctl/v1/bzerolib/error"
	bzcrt "bastionzero.com/bctl/v1/bzerolib/keysplitting/bzcert"
	ksmsg "bastionzero.com/bctl/v1/bzerolib/keysplitting/message"
	"bastionzero.com/bctl/v1/bzerolib/keysplitting/util"
	"bastionzero.com/bctl/v1/bzerolib/logger"
	"bastionzero.com/bctl/v1/bzerolib/tests"

	"github.com/Masterminds/semver"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestDaemonKeysplitting(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Daemon keysplitting suite")
}

var _ = Describe("Daemon keysplitting", func() {
	var sut *Keysplitting
	var agentKeypair *tests.Ed25519KeyPair
	var daemonKeypair *tests.Ed25519KeyPair
	var mockTokenRefresher *mocks.TokenRefresher
	var fakeZliKsConfig *tokenrefresh.ZLIKeysplittingConfig
	var agentSchemaVersion string
	const testAction string = "test/action"
	const prePipeliningVersion string = "1.9"
	const timeToPollNothingReceivedOnOutbox time.Duration = 500 * time.Millisecond

	GetAgentSchemaVersionAsSemVer := func() *semver.Version {
		parsedSchemaVersion, err := semver.NewVersion(agentSchemaVersion)
		Expect(err).ShouldNot(HaveOccurred())
		return parsedSchemaVersion
	}

	// Get the BZCert the daemon is expected to use given our faked ZLI
	// keysplitting configuration
	GetFakeBZCert := func() *bzcrt.BZCert {
		return &bzcrt.BZCert{
			InitialIdToken:  fakeZliKsConfig.KSConfig.InitialIdToken,
			CurrentIdToken:  fakeZliKsConfig.TokenSet.CurrentIdToken,
			ClientPublicKey: fakeZliKsConfig.KSConfig.PublicKey,
			Rand:            fakeZliKsConfig.KSConfig.CerRand,
			SignatureOnRand: fakeZliKsConfig.KSConfig.CerRandSignature,
		}
	}

	// Helper build agent message funcs
	BuildSynAckWithPayload := func(synMsg *ksmsg.KeysplittingMessage, payload []byte) *ksmsg.KeysplittingMessage {
		synAckMsg, err := synMsg.BuildUnsignedSynAck(
			payload,
			agentKeypair.Base64EncodedPublicKey,
			util.Nonce(),
			GetAgentSchemaVersionAsSemVer().String(),
		)
		Expect(err).ShouldNot(HaveOccurred())
		return &synAckMsg
	}
	BuildSynAck := func(synMsg *ksmsg.KeysplittingMessage) *ksmsg.KeysplittingMessage {
		return BuildSynAckWithPayload(synMsg, []byte{})
	}
	BuildDataAckWithPayload := func(dataMsg *ksmsg.KeysplittingMessage, payload []byte) *ksmsg.KeysplittingMessage {
		dataAckMsg, err := dataMsg.BuildUnsignedDataAck(
			payload,
			agentKeypair.Base64EncodedPublicKey,
			GetAgentSchemaVersionAsSemVer().String(),
		)
		Expect(err).ShouldNot(HaveOccurred())
		return &dataAckMsg
	}
	BuildDataAck := func(dataMsg *ksmsg.KeysplittingMessage) *ksmsg.KeysplittingMessage {
		return BuildDataAckWithPayload(dataMsg, []byte{})
	}
	SignAgentMsg := func(agentMsg *ksmsg.KeysplittingMessage) {
		err := agentMsg.Sign(agentKeypair.Base64EncodedPrivateKey)
		Expect(err).ShouldNot(HaveOccurred())
	}
	// Use this helper method to quickly validate messages, so another message
	// can be received. Please prefer to call Validate() directly (and not use
	// this function) when the It() is explictly asserting validation.
	ValidateAgentMsg := func(agentMsg *ksmsg.KeysplittingMessage) {
		err := sut.Validate(agentMsg)
		Expect(err).ShouldNot(HaveOccurred())
	}

	// Helper build daemon message funcs
	SendSynWithPayload := func(payload []byte) *ksmsg.KeysplittingMessage {
		// We must mock the token refresher, so that we can call BuildSyn
		fakeZliKsConfig = &tokenrefresh.ZLIKeysplittingConfig{
			KSConfig: tokenrefresh.KeysplittingConfig{
				PublicKey:        daemonKeypair.Base64EncodedPublicKey,
				PrivateKey:       daemonKeypair.Base64EncodedPrivateKey,
				CerRand:          "dummyCerRand",
				CerRandSignature: "dummyCerRandSignature",
				InitialIdToken:   "dummyInitialIdToken",
			},
			TokenSet: tokenrefresh.ZLITokenSetConfig{
				CurrentIdToken: "dummyCurrentIdToken",
			},
		}
		mockTokenRefresher.On("Refresh").Return(fakeZliKsConfig, nil)

		synMsg, err := sut.BuildSyn(testAction, payload, true)
		Expect(err).ShouldNot(HaveOccurred(), "building Syn should not error")

		Expect(sut.Outbox()).Should(Receive(Equal(synMsg)), "outbox should receive the Syn message sent by BuildSyn()")
		Expect(synMsg.Type).To(Equal(ksmsg.Syn))

		return synMsg
	}
	SendSyn := func() *ksmsg.KeysplittingMessage {
		return SendSynWithPayload([]byte{})
	}
	SendDataWithPayload := func(payload []byte) *ksmsg.KeysplittingMessage {
		err := sut.Inbox(testAction, payload)
		Expect(err).ShouldNot(HaveOccurred(), "sending Data should not error")

		var dataMsg *ksmsg.KeysplittingMessage
		Expect(sut.Outbox()).Should(Receive(&dataMsg), "outbox should receive the Data message sent by Inbox()")
		Expect(dataMsg.Type).To(Equal(ksmsg.Data))

		return dataMsg
	}
	SendData := func() *ksmsg.KeysplittingMessage {
		return SendDataWithPayload([]byte{})
	}

	// PerformHandshake completes the keysplitting handshake by sending a Syn
	// and receiving a valid SynAck. Returns the synAck message received.
	PerformHandshake := func() *ksmsg.KeysplittingMessage {
		synMsg := SendSyn()
		synAck := BuildSynAck(synMsg)
		SignAgentMsg(synAck)

		// We must validate and process the SynAck, so the pipeline lock can be
		// released and Inbox() can be called without blocking
		ValidateAgentMsg(synAck)
		return synAck
	}

	// Setup SUT that is used by all tests
	BeforeEach(func() {
		// Setup keypairs to use for agent and daemon
		var err error
		agentKeypair, err = tests.GenerateEd25519Key()
		Expect(err).ShouldNot(HaveOccurred())
		daemonKeypair, err = tests.GenerateEd25519Key()
		Expect(err).ShouldNot(HaveOccurred())

		// Set schema version to use when building agent messages
		agentSchemaVersion = ksmsg.SchemaVersion

		// Setup mocks here
		mockTokenRefresher = &mocks.TokenRefresher{}

		// Configure the SUT's logger to print to Ginkgo's writer
		ginkgoLogger, err := logger.New(logger.DefaultLoggerConfig(logger.Trace.String()), "/dev/null", []io.Writer{GinkgoWriter})
		Expect(err).ShouldNot(HaveOccurred())

		// Init the SUT
		sut, err = New(ginkgoLogger, agentKeypair.Base64EncodedPublicKey, mockTokenRefresher)
		Expect(err).ShouldNot(HaveOccurred())
	})

	AfterEach(func() {
		mockTokenRefresher.AssertExpectations(GinkgoT())
	})

	Describe("validate agent messages", func() {
		var msgUnderTest *ksmsg.KeysplittingMessage

		CommonAssertFailedBehavior := func() {
			It("validate fails when the message is unsigned", func() {
				msgUnderTest.Signature = ""
				err := sut.Validate(msgUnderTest)
				Expect(err).Should(MatchError(ErrInvalidSignature))
			})
		}

		Context("when agent message is not built on previously sent daemon message", func() {
			AssertFailedBehavior := func() {
				It("validate fails with unknown hpointer error", func() {
					err := sut.Validate(msgUnderTest)
					Expect(err).Should(MatchError(ErrUnknownHPointer))
				})
			}

			JustBeforeEach(func() {
				By(fmt.Sprintf("Signing %v without error", msgUnderTest.Type))
				SignAgentMsg(msgUnderTest)
			})

			Context("when the message is a SynAck-->Syn", func() {
				BeforeEach(func() {
					unknownSyn := &ksmsg.KeysplittingMessage{
						Type:                ksmsg.Syn,
						KeysplittingPayload: ksmsg.SynPayload{},
					}
					By("Building an unsigned SynAck without error")
					msgUnderTest = BuildSynAck(unknownSyn)
				})

				Describe("failure modes", func() {
					CommonAssertFailedBehavior()
					AssertFailedBehavior()
				})
			})

			Context("when the message is a DataAck-->Data", func() {
				BeforeEach(func() {
					unknownData := &ksmsg.KeysplittingMessage{
						Type:                ksmsg.Data,
						KeysplittingPayload: ksmsg.DataPayload{},
					}
					By("Building an unsigned DataAck without error")
					msgUnderTest = BuildDataAck(unknownData)
				})

				Describe("failure modes", func() {
					CommonAssertFailedBehavior()
					AssertFailedBehavior()
				})
			})
		})

		Context("when agent message is built on previously sent daemon message", func() {
			AssertBehavior := func() {
				It("validate succeeds when the message is signed", func() {
					By(fmt.Sprintf("Signing %v without error", msgUnderTest.Type))
					SignAgentMsg(msgUnderTest)

					By("Validating without error")
					err := sut.Validate(msgUnderTest)
					Expect(err).ShouldNot(HaveOccurred())
				})

				// Remove this test once CWC-1553 is addressed
				It("validate succeeds when the message is signed by a legacy agent (CWC-1553)", func() {
					By(fmt.Sprintf("Signing %v without error", msgUnderTest.Type))
					SignAgentMsg(msgUnderTest)

					By("Create different agent keypair than the one used when signing messages")
					diffAgentKeypair, err := tests.GenerateEd25519Key()
					Expect(err).ShouldNot(HaveOccurred())

					// Modify the SUT for just this It node with a different
					// agent pubkey than the one used when signing messages
					By("Modifying the SUT with this different agent keypair")
					sut.agentPubKey = diffAgentKeypair.Base64EncodedPublicKey

					By("Validating without error")
					err = sut.Validate(msgUnderTest)
					Expect(err).ShouldNot(HaveOccurred())
				})
			}

			Context("when the message is a SynAck-->Syn", func() {
				BeforeEach(func() {
					By("Sending Syn without error")
					synMsg := SendSyn()
					By("Building an unsigned SynAck without error")
					msgUnderTest = BuildSynAck(synMsg)
				})

				Describe("the happy path", func() {
					AssertBehavior()
				})

				Describe("failure modes", func() {
					CommonAssertFailedBehavior()

					// Schema version parsing only happens when message is a
					// SynAck
					It("validate fails when schema version cannot be parsed", func() {
						By("Modifying schema version to be invalid")
						synAckPayload, _ := msgUnderTest.KeysplittingPayload.(ksmsg.SynAckPayload)
						synAckPayload.SchemaVersion = "bad-version"
						msgUnderTest.KeysplittingPayload = synAckPayload

						By(fmt.Sprintf("Signing %v without error", msgUnderTest.Type))
						SignAgentMsg(msgUnderTest)
						err := sut.Validate(msgUnderTest)
						Expect(err).Should(MatchError(ErrFailedToParseVersion))
					})
				})
			})

			Context("when the message is a DataAck", func() {
				// Builds a DataAck-->Data-->SynAck-->Syn
				BuildDataAckForDataAfterHandshake := func() *ksmsg.KeysplittingMessage {
					By("Performing handshake without error")
					PerformHandshake()

					By("Sending a Data msg without error")
					sentDataMsg := SendData()
					By("Building an unsigned DataAck without error")
					return BuildDataAck(sentDataMsg)
				}

				Context("when the message is a DataAck-->Data-->SynAck-->Syn", func() {
					BeforeEach(func() {
						msgUnderTest = BuildDataAckForDataAfterHandshake()
					})

					Describe("the happy path", func() {
						AssertBehavior()
					})

					Describe("failure modes", func() {
						CommonAssertFailedBehavior()
					})
				})

				Context("when the message is a DataAck-->Data-->DataAck-->Data-->SynAck-->Syn", func() {
					BeforeEach(func() {
						firstDataAck := BuildDataAckForDataAfterHandshake()
						By("Signing first DataAck without error")
						SignAgentMsg(firstDataAck)
						By("Validating first DataAck without error")
						ValidateAgentMsg(firstDataAck)

						By("Sending second Data msg without error")
						sentDataMsg := SendData()
						By("Building second, unsigned DataAck without error")
						msgUnderTest = BuildDataAck(sentDataMsg)
					})

					Describe("the happy path", func() {
						AssertBehavior()
					})

					Describe("failure modes", func() {
						CommonAssertFailedBehavior()
					})
				})
			})
		})
	})

	Describe("send Syn", func() {
		Describe("the happy path", func() {
			AssertBehavior := func() {
				It("Syn is built correctly", func() {
					By("Sending Syn without error")
					payload := []byte{}
					synMsg := SendSynWithPayload(payload)

					By("Asserting the keysplitting message is correct")
					Expect(synMsg.Type).To(Equal(ksmsg.Syn))
					Expect(synMsg.Signature).NotTo(BeEmpty())
					synPayload, ok := synMsg.KeysplittingPayload.(ksmsg.SynPayload)
					Expect(ok).To(BeTrue())

					By("Asserting the keysplitting message payload details are correct")
					Expect(synPayload.SchemaVersion).To(Equal(ksmsg.SchemaVersion))
					Expect(synPayload.Type).To(BeEquivalentTo(ksmsg.Syn))
					Expect(synPayload.Action).To(Equal(testAction))
					// TODO-Yuval: Discuss this assertion with Lucie
					Expect(synPayload.ActionPayload).To(BeEquivalentTo(fmt.Sprintf("\"%v\"", base64.StdEncoding.EncodeToString(payload))))
					Expect(synPayload.TargetId).To(Equal(agentKeypair.Base64EncodedPublicKey))
					Expect(synPayload.Nonce).NotTo(BeEmpty())
					Expect(synPayload.BZCert).To(Equal(*GetFakeBZCert()))

					By("Asserting the message signature validates")
					Expect(synMsg.VerifySignature(daemonKeypair.Base64EncodedPublicKey)).ShouldNot(HaveOccurred())
				})
			}

			Context("when private key is 32 bytes", func() {
				BeforeEach(func() {
					// For this context only, set the private key to 32 bytes
					daemonKeypair.Base64EncodedPrivateKey = base64.StdEncoding.EncodeToString(daemonKeypair.PrivateKey[:32])
				})

				AssertBehavior()
			})

			Context("when private key is not 32 bytes", func() {
				// There is nothing extra to setup because our test suite by
				// default uses a private key that is not 32 bytes
				AssertBehavior()
			})
		})

		Describe("failure modes", func() {
			var synMsg *ksmsg.KeysplittingMessage
			var buildSynError error

			JustBeforeEach(func() {
				synMsg, buildSynError = sut.BuildSyn(testAction, []byte{}, true)
				Expect(synMsg).To(BeNil())
			})

			AssertFailedBehavior := func() {
				It("Syn is not sent to outbox", func() {
					Expect(sut.Outbox()).ShouldNot(Receive())
				})
			}

			Context("when token refresh fails", func() {
				var refreshError error
				BeforeEach(func() {
					By("Mocking the token refresher to return an error")
					refreshError = errors.New("refresh error")
					mockTokenRefresher.On("Refresh").Return(nil, refreshError)
				})

				It("errors", func() {
					Expect(buildSynError).To(MatchError(refreshError))
				})

				AssertFailedBehavior()
			})

			Context("when signing fails", func() {
				BeforeEach(func() {
					By("Mocking the token refresher to return a config with an invalid signing key")
					fakeZliKsConfig = &tokenrefresh.ZLIKeysplittingConfig{
						KSConfig: tokenrefresh.KeysplittingConfig{
							PrivateKey: "badkey",
						},
					}
					mockTokenRefresher.On("Refresh").Return(fakeZliKsConfig, nil)
				})

				It("errors", func() {
					Expect(buildSynError).To(MatchError(ErrFailedToSign))
				})

				AssertFailedBehavior()
			})
		})
	})

	Describe("send Data", func() {
		Context("when handshake is not complete", func() {
			var synMsg *ksmsg.KeysplittingMessage

			BeforeEach(func() {
				By("Sending Syn without error")
				synMsg = SendSyn()
			})

			It("cannot send Data", func() {
				done := make(chan interface{})
				go func() {
					defer GinkgoRecover()

					By("Sending a message that causes Inbox() to block because handshake is not complete")
					SendData()

					close(done)
				}()

				// Check that nothing is received on Outbox for some fixed
				// duration
				Consistently(sut.Outbox(), timeToPollNothingReceivedOnOutbox).ShouldNot(Receive(), "no message should be sent to outbox because the handshake never completed")

				// Complete the handshake by validating a SynAck so the
				// goroutine spawned above can unblock and terminate
				synAck := BuildSynAck(synMsg)
				SignAgentMsg(synAck)
				ValidateAgentMsg(synAck)

				Eventually(done).Should(BeClosed(), "done should eventually be closed because agent sent a SynAck, which completes the handshake, and should unblock Inbox()")
			})
		})

		Context("when handshake is complete", func() {
			var synAck *ksmsg.KeysplittingMessage

			Describe("the happy path", func() {
				JustBeforeEach(func() {
					// We must perform a successful handshake before we can attempt
					// to send a Data msg.
					By("Performing handshake without error")
					synAck = PerformHandshake()
				})

				AssertBehavior := func(prePipelining bool) {
					It("Data is built correctly", func() {
						payload := []byte{}
						By("Sending a Data msg without error")
						dataMsg := SendDataWithPayload(payload)
						expectedPrevMessage := synAck

						By("Asserting the keysplitting message is correct")
						Expect(dataMsg.Type).To(Equal(ksmsg.Data))
						Expect(dataMsg.Signature).NotTo(BeEmpty())
						dataPayload, ok := dataMsg.KeysplittingPayload.(ksmsg.DataPayload)
						Expect(ok).To(BeTrue())

						By("Asserting the keysplitting message payload details are correct")
						Expect(dataPayload.SchemaVersion).To(Equal(GetAgentSchemaVersionAsSemVer().String()), "The schema version should match the agreed upon version found in the agent's SynAck")
						Expect(dataPayload.Type).To(BeEquivalentTo(ksmsg.Data))
						Expect(dataPayload.Action).To(Equal(testAction))
						Expect(dataPayload.TargetId).To(Equal(agentKeypair.Base64EncodedPublicKey))
						// Asserts that Validate() was called for the previous Ack
						// sent by the agent. If true, then this Data msg points to
						// the correct message in the chain
						Expect(dataPayload.HPointer).Should(Equal(expectedPrevMessage.Hash()), fmt.Sprintf("This Data msg's HPointer should point to the previously received message: %#v", expectedPrevMessage))
						if prePipelining {
							// TODO: Remove when CWC-1820 is resolved
							Expect(dataPayload.ActionPayload).To(BeEquivalentTo(fmt.Sprintf("\"%v\"", base64.StdEncoding.EncodeToString(payload))), "Pre-pipelining includes extra quotes")
						} else {
							Expect(dataPayload.ActionPayload).To(Equal(payload))
						}
						expectedBzCertHash, ok := GetFakeBZCert().Hash()
						Expect(ok).Should(BeTrue(), "There should not be an error when hashing the expected BZCert")
						Expect(dataPayload.BZCertHash).To(Equal(expectedBzCertHash))

						By("Asserting the message signature validates")
						Expect(dataMsg.VerifySignature(daemonKeypair.Base64EncodedPublicKey)).ShouldNot(HaveOccurred())
					})
				}

				Context("when private key is 32 bytes", func() {
					BeforeEach(func() {
						// For this context only, set the private key to 32 bytes
						daemonKeypair.Base64EncodedPrivateKey = base64.StdEncoding.EncodeToString(daemonKeypair.PrivateKey[:32])
					})

					AssertBehavior(false)
				})

				Context("when private key is not 32 bytes", func() {
					// There is nothing extra to setup because our test suite by
					// default uses a private key that is not 32 bytes
					AssertBehavior(false)
				})

				Context("when version is pre-pipelining agent (CWC-1820)", func() {
					BeforeEach(func() {
						// For this context only, set the schema version to
						// something prior to pipelining
						agentSchemaVersion = prePipeliningVersion
					})

					AssertBehavior(true)
				})
			})

			Describe("failure modes", func() {
				BeforeEach(func() {
					// We must perform a successful handshake before we can attempt
					// to send a Data msg.
					By("Performing handshake without error")
					synAck = PerformHandshake()
				})

				AssertFailedBehavior := func() {
					It("Data is not sent to outbox", func() {
						Expect(sut.Outbox()).ShouldNot(Receive())
					})
				}

				Context("when signing fails", func() {
					var inboxError error

					JustBeforeEach(func() {
						inboxError = sut.Inbox(testAction, []byte{})
					})

					BeforeEach(func() {
						// It would be better not to set the private field and only
						// use public functions of the SUT, but we have to perform a
						// valid handshake before we can call Inbox(). We can't set
						// the token refresher mock to give a bad key, otherwise the
						// Syn would fail to send.
						By("Setting client secret key to bad key")
						sut.clientSecretKey = "badkey"
					})

					It("errors", func() {
						Expect(inboxError).To(MatchError(ErrFailedToSign))
					})

					AssertFailedBehavior()
				})

				Context("when action is not specified", func() {
					var inboxError error

					JustBeforeEach(func() {
						// Don't specify action
						inboxError = sut.Inbox("", []byte{})
					})

					It("errors", func() {
						Expect(inboxError).Should(HaveOccurred())
					})

					AssertFailedBehavior()
				})
			})
		})
	})

	Describe("pipelining", func() {
		AssertDataMsgIsCorrect := func(dataMsg *ksmsg.KeysplittingMessage, expectedPayload []byte, expectedPrevMessage *ksmsg.KeysplittingMessage) {
			dataPayload, ok := dataMsg.KeysplittingPayload.(ksmsg.DataPayload)
			Expect(ok).To(BeTrue(), "passed in message must be a Data msg")
			Expect(dataPayload.HPointer).Should(Equal(expectedPrevMessage.Hash()), fmt.Sprintf("This Data msg's HPointer should point to the previously received message: %#v", expectedPrevMessage))
			Expect(dataPayload.ActionPayload).To(Equal(expectedPayload), "The Data's payload should match the expected payload")
			Expect(dataPayload.SchemaVersion).To(Equal(GetAgentSchemaVersionAsSemVer().String()), "The schema version should match the agreed upon version found in the agent's SynAck")
		}

		BuildErrorMessage := func(hPointer string) rrr.ErrorMessage {
			return rrr.ErrorMessage{
				SchemaVersion: rrr.CurrentVersion,
				Timestamp:     time.Now().Unix(),
				Type:          string(rrr.KeysplittingValidationError),
				Message:       "agent error message",
				HPointer:      hPointer,
			}
		}

		// Remove this context when CWC-1820 is resolved
		Context("when pipelining is disabled (CWC-1820)", func() {
			var dataMsg *ksmsg.KeysplittingMessage
			BeforeEach(func() {
				agentSchemaVersion = prePipeliningVersion
				By("Performing handshake without error")
				synAck := PerformHandshake()

				// Send some Data
				By("Sending a Data msg without error")
				dataMsg = SendData()
				// Payload contains extra quotes because this is pre-pipelining
				AssertDataMsgIsCorrect(dataMsg, []byte("\"\""), synAck)
			})

			Context("when there is a single outstanding DataAck", func() {
				// There is nothing to configure because the outer BeforeEach
				// already sent a Data that has an outstanding DataAck.

				It("block sending Data until DataAck is received", func() {
					done := make(chan interface{})
					var dataAck *ksmsg.KeysplittingMessage
					go func() {
						defer GinkgoRecover()

						By("Sending a message that causes Inbox() to block")
						dataSentAfterUnblocking := SendData()

						// dataAck is initialized after unblocking
						By("Asserting Data is correct after being unblocked")
						AssertDataMsgIsCorrect(dataSentAfterUnblocking, []byte("\"\""), dataAck)

						close(done)
					}()

					// Check that nothing is received on Outbox for some fixed
					// duration
					Consistently(sut.Outbox(), timeToPollNothingReceivedOnOutbox).ShouldNot(Receive(), "no message should be sent to outbox because there is an outstanding DataAck")

					// Validate the DataAck so the goroutine spawned above can
					// unblock and terminate
					dataAck = BuildDataAck(dataMsg)
					SignAgentMsg(dataAck)
					ValidateAgentMsg(dataAck)

					Eventually(done).Should(BeClosed(), "done should eventually be closed because agent sent DataAck to unblock pipeline")
				})
			})
		})

		Context("when pipelining is enabled", func() {
			Describe("the happy path", func() {
				type sentKeysplittingData struct {
					sentPayload []byte
					sentMsg     *ksmsg.KeysplittingMessage
				}

				Describe("send Data messages when there are outstanding DataAcks", func() {
					var firstDataMsg *ksmsg.KeysplittingMessage
					// Holds sent data that is based on predicted DataAcks, i.e.
					// called with SendDataAndTrack() functions
					var sentData []*sentKeysplittingData

					SendDataAndTrack := func(id int) {
						By(fmt.Sprintf("Sending Data(%v)", id))
						payload := []byte(fmt.Sprintf("Data msg - #%v", id))
						dataMsg := SendDataWithPayload(payload)
						sentData = append(sentData, &sentKeysplittingData{
							sentPayload: payload,
							sentMsg:     dataMsg,
						})
					}
					RepeatSendDataAndTrack := func(count int) {
						for i := 1; i <= count; i++ {
							SendDataAndTrack(i)
						}
					}

					AssertSendingDataBehavior := func() {
						It("all Data based on predicted DataAcks are sent and built correctly", func() {
							prevMsg := firstDataMsg
							for i := 0; i < len(sentData); i++ {
								By(fmt.Sprintf("Asserting Data(%v)-->predicted DataAck(Data(%v)) is built correctly", i+1, i))

								// Data points to a predicted DataAck for
								// prevMsg
								By(fmt.Sprintf("Building predicted DataAck(Data(%v))", i))
								predictedDataAck := BuildDataAck(prevMsg)

								currMsg := sentData[i]
								By(fmt.Sprintf("Asserting Data(%v) is correct", i+1))
								AssertDataMsgIsCorrect(currMsg.sentMsg, currMsg.sentPayload, predictedDataAck)

								// Update pointer
								prevMsg = currMsg.sentMsg
							}
						})
					}

					BeforeEach(func() {
						// Initalize slice to prevent specs from leaking into
						// one another
						sentData = make([]*sentKeysplittingData, 0)

						// Perform a successful handshake before attempting to
						// send Data
						By("Performing handshake without error")
						synAck := PerformHandshake()

						// Send a single Data-->SynAck, so we can start sending
						// Data that is built on outstanding DataAcks, starting
						// with the DataAck for this first Data message
						By("Sending Data(0)")
						payload := []byte("Data msg - #0")
						firstDataMsg = SendDataWithPayload(payload)
						By("Asserting Data(0) is built correctly")
						AssertDataMsgIsCorrect(firstDataMsg, payload, synAck)
					})

					Context("when 1 DataAck is outstanding", func() {
						BeforeEach(func() {
							RepeatSendDataAndTrack(1)
						})

						AssertSendingDataBehavior()
					})

					Context("when 3 DataAcks are outstanding", func() {
						BeforeEach(func() {
							RepeatSendDataAndTrack(3)
						})

						AssertSendingDataBehavior()
					})

					Context("when the max pipelining limit is reached", func() {
						BeforeEach(func() {
							// Send enough Data to fill the pipeline to its max
							// capacity
							RepeatSendDataAndTrack(PipelineLimit - 1)

							// Send the message that causes Inbox() to block
							// (because pipeline is full) on a separate
							// goroutine, so we can unblock it on the main
							// thread
							done := make(chan interface{})
							go func() {
								defer GinkgoRecover()

								By("Sending a message that causes Inbox() to block because pipeline is full")
								SendDataAndTrack(PipelineLimit)

								// We only close the channel once
								// SendDataAndTrack() returns.
								// SendDataAndTrack() returns only once Inbox()
								// unblocks and we see the message on the
								// outbox.
								close(done)
							}()

							// Check that nothing is received on Outbox for some
							// fixed duration
							Consistently(sut.Outbox(), timeToPollNothingReceivedOnOutbox).ShouldNot(Receive(), "no message should be sent to outbox because pipeline is full")

							// Build DataAck for first Data msg sent
							dataAckForFirstDataMsg := BuildDataAck(firstDataMsg)
							SignAgentMsg(dataAckForFirstDataMsg)
							// Pipeline lock is released when we call Validate()
							ValidateAgentMsg(dataAckForFirstDataMsg)

							Eventually(done).Should(BeClosed(), "done should eventually be closed because agent sent DataAck to unblock pipeline")
						})

						AssertSendingDataBehavior()
					})
				})

				Describe("recovery", func() {
					BeforeEach(func() {
						// Perform a successful handshake before attempting to
						// send Data
						By("Performing handshake without error")
						PerformHandshake()
					})

					Context("when recovery handshake does not complete", func() {
						var synMsg *ksmsg.KeysplittingMessage

						BeforeEach(func() {
							// Send Data, so that recovery procedure has
							// something to resend
							By("Sending Data")
							dataMsg := SendData()

							By("Starting recovery procedure without error")
							agentErrorMessage := BuildErrorMessage(dataMsg.Hash())
							err := sut.Recover(agentErrorMessage)
							Expect(err).ShouldNot(HaveOccurred())

							// Grab the Syn that Recover() pushes to outbox, so
							// that outbox remains empty for this context
							By("Pushing the Syn message created during recovery to the outbox")
							Expect(sut.Outbox()).Should(Receive(&synMsg))
							Expect(synMsg.Type).Should(Equal(ksmsg.Syn))
						})

						It("cannot send Data", func() {
							done := make(chan interface{})
							go func() {
								defer GinkgoRecover()

								By("Sending a message that causes Inbox() to block because recovery handshake is not complete")
								SendData()

								close(done)
							}()

							// Check that nothing is received on Outbox for some fixed
							// duration
							Consistently(sut.Outbox(), timeToPollNothingReceivedOnOutbox).ShouldNot(Receive(), "no message should be sent to outbox because the recovery handshake never completed")

							// Complete the handshake by validating a SynAck so
							// the goroutine spawned above can unblock and
							// terminate
							synAck := BuildSynAck(synMsg)
							SignAgentMsg(synAck)
							ValidateAgentMsg(synAck)

							Eventually(done).Should(BeClosed(), "done should eventually be closed because agent sent a SynAck, which completes the recovery handshake, and should unblock Inbox()")
						})
					})

					Context("when recovery handshake completes", func() {
						// Holds *all* payloads and Data messages sent prior to
						// recovery
						var sentData []*sentKeysplittingData
						var amountOfDataMsgsToSend int = 3
						GetSentPayloads := func() [][]byte {
							sentPayloads := make([][]byte, 0)
							for _, sentDataMsg := range sentData {
								sentPayloads = append(sentPayloads, sentDataMsg.sentPayload)
							}
							return sentPayloads
						}

						var synMsgSentDuringRecovery *ksmsg.KeysplittingMessage
						// synAck to the synMsg sent during recovery
						var recoverySynAck *ksmsg.KeysplittingMessage

						AssertBehavior := func(sliceFromIndex int) {
							It(fmt.Sprintf("payloads ranging from [%v:%v) are resent in new Data messages", sliceFromIndex, amountOfDataMsgsToSend), func() {
								// prevMsg is set after first iteration of for loop
								// below
								var prevMsg *ksmsg.KeysplittingMessage
								for i, payload := range GetSentPayloads()[sliceFromIndex:] {
									var dataMsg *ksmsg.KeysplittingMessage
									Expect(sut.Outbox()).Should(Receive(&dataMsg))
									Expect(dataMsg.Type).Should(Equal(ksmsg.Data))

									By(fmt.Sprintf("Asserting Data msg containing payload %q is resent", payload))
									if i == 0 {
										// The first data message points to the recovery
										// syn ack
										AssertDataMsgIsCorrect(dataMsg, payload, recoverySynAck)
									} else {
										// All other data messages point to predicted
										// DataAck for prevMsg
										predictedDataAck := BuildDataAck(prevMsg)
										AssertDataMsgIsCorrect(dataMsg, payload, predictedDataAck)
									}

									// Update pointer
									prevMsg = dataMsg
								}

								// There should be no more Data on the outbox
								// because we should have read them all in the for
								// loop above. If there are extra Data messages, it
								// means recovery sent extra payloads that should
								// not have been resent.
								By("Asserting no other Data messages are pushed to the outbox")
								Consistently(sut.Outbox(), timeToPollNothingReceivedOnOutbox).ShouldNot(Receive())
							})
						}

						BeforeEach(func() {
							// Initalize slice to prevent specs from leaking into
							// one another
							sentData = make([]*sentKeysplittingData, 0)

							// Send some Data, so that recovery procedure has
							// something to resend
							for i := 0; i < amountOfDataMsgsToSend; i++ {
								payload := []byte(fmt.Sprintf("Data msg - #%v", i))
								By(fmt.Sprintf("Sending Data(%v)", i))
								dataMsg := SendDataWithPayload(payload)
								sentData = append(sentData, &sentKeysplittingData{
									sentPayload: payload,
									sentMsg:     dataMsg,
								})
							}

							// Build error message that refers to first Data msg
							// sent. There is *no* requirement to have the error
							// message refer to a specific Data message because we
							// also control the SynAck (and nonce) which governs
							// which Data messages to resend. We only need the error
							// message to refer to some Data message that still
							// exists in pipelineMap, so that calling Recover()
							// succeeds without error.
							agentErrorMessage := BuildErrorMessage(sentData[0].sentMsg.Hash())
							// Starts the recovery procedure by sending a new Syn
							By("Starting recovery procedure without error")
							err := sut.Recover(agentErrorMessage)
							Expect(err).ShouldNot(HaveOccurred())

							// Recover() sends a Syn
							By("Pushing the Syn message created during recovery to the outbox")
							Expect(sut.Outbox()).Should(Receive(&synMsgSentDuringRecovery))
							Expect(synMsgSentDuringRecovery.Type).Should(Equal(ksmsg.Syn))
						})

						JustBeforeEach(func() {
							By("Signing agent's recovery SynAck without error")
							SignAgentMsg(recoverySynAck)
							By("Validating agent's recovery SynAck without error")
							// Completes the recovery procedure by triggering
							// resend() of all previously sent Data messages
							ValidateAgentMsg(recoverySynAck)
						})

						Context("when recovery SynAck's schema version is different than the one agreed upon in initial handshake", func() {
							BeforeEach(func() {
								By("Building agent's recovery SynAck with a different schema version than before")
								agentSchemaVersion = agentSchemaVersion + "-different"
								// The default SynAck created by BuildSynAck()
								// uses a random nonce
								recoverySynAck = BuildSynAck(synMsgSentDuringRecovery)
							})

							// Pass index 0 because all payloads should be resent
							AssertBehavior(0)
						})

						Context("when recovery SynAck's nonce references message not known by daemon", func() {
							BeforeEach(func() {
								By("Building agent's recovery SynAck without error")
								// The default SynAck created by BuildSynAck() uses
								// a random nonce
								recoverySynAck = BuildSynAck(synMsgSentDuringRecovery)
							})

							// Pass index 0 because all payloads should be resent
							AssertBehavior(0)
						})

						Context("when recovery SynAck's nonce references message known by daemon", func() {
							BeforeEach(func() {
								By("Building agent's recovery SynAck without error")
								recoverySynAck = BuildSynAck(synMsgSentDuringRecovery)
							})

							Context("when referenced message is first Data sent", func() {
								BeforeEach(func() {
									By("Modifying recovery SynAck's nonce to refer to first Data message sent")
									recoverySynAckPayload, _ := recoverySynAck.KeysplittingPayload.(ksmsg.SynAckPayload)
									recoverySynAckPayload.Nonce = sentData[0].sentMsg.Hash()
									recoverySynAck.KeysplittingPayload = recoverySynAckPayload
								})

								// Pass index 1 because the first payload (index 0)
								// sent should not be resent
								AssertBehavior(1)
							})

							Context("when referenced message is last Data sent", func() {
								BeforeEach(func() {
									By("Modifying recovery SynAck's nonce to refer to last Data message sent")
									recoverySynAckPayload, _ := recoverySynAck.KeysplittingPayload.(ksmsg.SynAckPayload)
									recoverySynAckPayload.Nonce = sentData[len(sentData)-1].sentMsg.Hash()
									recoverySynAck.KeysplittingPayload = recoverySynAckPayload
								})

								It("no Data is resent", func() {
									Consistently(sut.Outbox(), timeToPollNothingReceivedOnOutbox).ShouldNot(Receive(), "because we resend messages starting with the one immediately after the referenced one")
								})
							})
						})
					})

				})
			})

			Describe("failure modes", func() {
				Describe("recovery", func() {
					var agentErrorMessage rrr.ErrorMessage
					var recoverError error

					SendDataAndBuildErrorMessage := func() rrr.ErrorMessage {
						By("Sending data and building an agent error message")
						dataMsg := SendDataWithPayload([]byte("agent fail on this data message"))
						return BuildErrorMessage(dataMsg.Hash())
					}

					JustBeforeEach(func() {
						recoverError = sut.Recover(agentErrorMessage)
					})

					AssertFailedBehavior := func() {
						It("no Syn message is sent", func() {
							Consistently(sut.Outbox(), timeToPollNothingReceivedOnOutbox).ShouldNot(Receive())
						})
					}

					Context("when agent error message hpointer is empty", func() {
						BeforeEach(func() {
							agentErrorMessage = BuildErrorMessage("")
						})

						AssertFailedBehavior()

						It("daemon is not recovering", func() {
							Expect(sut.Recovering()).Should(BeFalse())
						})

						It("errors", func() {
							Expect(recoverError).Should(HaveOccurred())
						})
					})

					Context("when agent error message hpointer refers to message not sent by daemon", func() {
						BeforeEach(func() {
							agentErrorMessage = BuildErrorMessage("unknown")
						})

						AssertFailedBehavior()

						It("daemon is not recovering", func() {
							Expect(sut.Recovering()).Should(BeFalse())
						})
					})

					Context("when agent error message hpointer refers to Syn message", func() {
						BeforeEach(func() {
							By("Sending Syn without error")
							synMsg := SendSyn()
							agentErrorMessage = BuildErrorMessage(synMsg.Hash())
						})

						AssertFailedBehavior()

						It("daemon is not recovering", func() {
							Expect(sut.Recovering()).Should(BeFalse())
						})

						It("errors", func() {
							Expect(recoverError).Should(HaveOccurred())
						})
					})

					Context("when daemon is already recovering", func() {
						BeforeEach(func() {
							By("Performing handshake without error")
							PerformHandshake()
							agentErrorMessage = SendDataAndBuildErrorMessage()

							// Recover once before we call Recover again in
							// JustBeforeEach()
							err := sut.Recover(agentErrorMessage)
							Expect(err).ShouldNot(HaveOccurred())

							// Grab the Syn message from the outbox, so that we
							// can assert no extra Syn is sent in this Context.
							var synMsg *ksmsg.KeysplittingMessage
							Expect(sut.Outbox()).Should(Receive(&synMsg))
							Expect(synMsg.Type).Should(Equal(ksmsg.Syn))
						})

						AssertFailedBehavior()

						It("daemon is still recovering", func() {
							Expect(sut.Recovering()).Should(BeTrue())
						})
					})

					Context("when recovery has already failed the max number of times", func() {
						BeforeEach(func() {
							By("Performing handshake without error")
							PerformHandshake()

							for i := 0; i < MaxErrorRecoveryTries; i++ {
								By(fmt.Sprintf("Recover() without error: #%v", i))
								agentErrorMessage := SendDataAndBuildErrorMessage()
								err := sut.Recover(agentErrorMessage)
								Expect(err).ShouldNot(HaveOccurred())

								By("Pushing the Syn msg to the outbox")
								var synMsg *ksmsg.KeysplittingMessage
								Expect(sut.Outbox()).Should(Receive(&synMsg))
								Expect(synMsg.Type).Should(Equal(ksmsg.Syn))

								// Call Validate() with a SynAck to have
								// recovering boolean reset allowing us to call
								// Recover() again
								synAck := BuildSynAck(synMsg)
								SignAgentMsg(synAck)
								ValidateAgentMsg(synAck)

								// Each time we valiate a SynAck, resend() is
								// called and all Data in pipeline map will be
								// resent. Read from the Outbox() the correct
								// number of times, so that the output channel
								// is empty for the next Recover() iteration.
								for j := 0; j <= i; j++ {
									var dataMsg *ksmsg.KeysplittingMessage
									Expect(sut.Outbox()).Should(Receive(&dataMsg))
									Expect(dataMsg.Type).Should(Equal(ksmsg.Data))
								}
							}

							agentErrorMessage = SendDataAndBuildErrorMessage()
						})

						AssertFailedBehavior()

						It("errors", func() {
							Expect(recoverError).Should(HaveOccurred())
						})
					})

					Context("when building Syn during recovery fails", func() {
						var refreshError error

						BeforeEach(func() {
							By("Performing handshake without error")
							PerformHandshake()
							agentErrorMessage = SendDataAndBuildErrorMessage()

							// Reset the token refresher mock after performing
							// successful handshake, so that we can return error
							mockTokenRefresher.ExpectedCalls = nil
							// Mock the token refresher to return error to cause
							// error when building Syn during recovery.
							By("Mocking the token refresher to return an error")
							refreshError = errors.New("refresh error")
							mockTokenRefresher.On("Refresh").Return(nil, refreshError)
						})

						AssertFailedBehavior()

						It("errors", func() {
							Expect(recoverError).To(MatchError(refreshError))
						})

						It("daemon is still recovering", func() {
							Expect(sut.Recovering()).Should(BeTrue())
						})
					})
				})
			})
		})
	})
})
