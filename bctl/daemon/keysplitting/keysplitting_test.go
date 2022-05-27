package keysplitting

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"testing"

	"bastionzero.com/bctl/v1/bctl/daemon/keysplitting/mocks"
	"bastionzero.com/bctl/v1/bctl/daemon/keysplitting/tokenrefresh"
	bzcrt "bastionzero.com/bctl/v1/bzerolib/keysplitting/bzcert"
	ksmsg "bastionzero.com/bctl/v1/bzerolib/keysplitting/message"
	"bastionzero.com/bctl/v1/bzerolib/keysplitting/util"
	"bastionzero.com/bctl/v1/bzerolib/logger"
	"bastionzero.com/bctl/v1/bzerolib/tests"

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
	testAction := "test/action"
	agentSchemaVersion := ksmsg.SchemaVersion

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
		By("Building an unsigned SynAck without error")
		synAckMsg, err := synMsg.BuildUnsignedSynAck(
			payload,
			agentKeypair.Base64EncodedPublicKey,
			util.Nonce(),
			agentSchemaVersion,
		)
		Expect(err).ShouldNot(HaveOccurred())
		return &synAckMsg
	}
	BuildSynAck := func(synMsg *ksmsg.KeysplittingMessage) *ksmsg.KeysplittingMessage {
		return BuildSynAckWithPayload(synMsg, []byte{})
	}
	BuildDataAckWithPayload := func(dataMsg *ksmsg.KeysplittingMessage, payload []byte) *ksmsg.KeysplittingMessage {
		By("Building an unsigned DataAck without error")
		dataAckMsg, err := dataMsg.BuildUnsignedDataAck(
			payload,
			agentKeypair.Base64EncodedPublicKey,
			agentSchemaVersion,
		)
		Expect(err).ShouldNot(HaveOccurred())
		return &dataAckMsg
	}
	BuildDataAck := func(dataMsg *ksmsg.KeysplittingMessage) *ksmsg.KeysplittingMessage {
		return BuildDataAckWithPayload(dataMsg, []byte{})
	}
	SignAgentMsg := func(agentMsg *ksmsg.KeysplittingMessage) {
		By(fmt.Sprintf("Signing %v without error", agentMsg.Type))
		err := agentMsg.Sign(agentKeypair.Base64EncodedPrivateKey)
		Expect(err).ShouldNot(HaveOccurred())
	}
	// Use this helper method to quickly validate messages, so another message
	// can be received. Please prefer to call Validate() directly (and not use
	// this function) when the It() is explictly asserting validation.
	ValidateAgentMsg := func(agentMsg *ksmsg.KeysplittingMessage) {
		By(fmt.Sprintf("Validating %v without error", agentMsg.Type))
		err := sut.Validate(agentMsg)
		Expect(err).ShouldNot(HaveOccurred())
	}

	// Helper build daemon message funcs
	SendSynWithPayload := func(payload []byte) *ksmsg.KeysplittingMessage {
		// We must mock the token refresher, so that we can call BuildSyn
		By("Mocking the token refresher to return a dummy KS config")
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

		By("Building a signed Syn without error")
		synMsg, err := sut.BuildSyn(testAction, payload, true)
		Expect(err).ShouldNot(HaveOccurred())

		By("Pushing the Syn msg to the outbox")
		Expect(sut.Outbox()).Should(Receive(Equal(synMsg)))

		return synMsg
	}
	SendSyn := func() *ksmsg.KeysplittingMessage {
		return SendSynWithPayload([]byte{})
	}
	SendDataWithPayload := func(payload []byte) *ksmsg.KeysplittingMessage {
		By("Sending a Data msg without error")
		err := sut.Inbox(testAction, payload)
		Expect(err).ShouldNot(HaveOccurred())

		By("Pushing the Data msg to the outbox")
		var dataMsg *ksmsg.KeysplittingMessage
		Eventually(sut.Outbox()).Should(Receive(&dataMsg))

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

					// TODO-Yuval: Find a way to assert this behavior generally
					// even when agent message *is* built on previously sent
					// daemon message. We currently can't do that because
					// Inbox() is locked because BuildSyn() is called without
					// receiving a valid message. It works here because we don't
					// call BuildSyn() in this entire Context.
					By("Attempting to send a message fails with error")
					err = sut.Inbox(testAction, []byte{})
					Expect(err).Should(MatchError(ErrMissingLastAck), "because we never received a valid ack, so there is no chain to continue from")
				})
			}

			JustBeforeEach(func() {
				SignAgentMsg(msgUnderTest)
			})

			Context("when the message is a SynAck-->Syn", func() {
				BeforeEach(func() {
					unknownSyn := &ksmsg.KeysplittingMessage{
						Type:                ksmsg.Syn,
						KeysplittingPayload: ksmsg.SynPayload{},
					}
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
					SignAgentMsg(msgUnderTest)

					By("Validating without error")
					err := sut.Validate(msgUnderTest)
					Expect(err).ShouldNot(HaveOccurred())
				})

				// Remove this test once CWC-1553 is addressed
				It("validate succeeds when the message is signed by a legacy agent (CWC-1553)", func() {
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
					synMsg := SendSyn()
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

						SignAgentMsg(msgUnderTest)
						err := sut.Validate(msgUnderTest)
						Expect(err).Should(MatchError(ErrFailedToParseVersion))
					})
				})
			})

			Context("when the message is a DataAck", func() {
				// Builds a DataAck-->Data-->SynAck-->Syn
				BuildDataAckForDataAfterHandshake := func() *ksmsg.KeysplittingMessage {
					PerformHandshake()

					sentDataMsg := SendData()
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
						SignAgentMsg(firstDataAck)
						ValidateAgentMsg(firstDataAck)

						sentDataMsg := SendData()
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
			It("Syn is built correctly", func() {
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
		var synAck *ksmsg.KeysplittingMessage

		BeforeEach(func() {
			// We must perform a successful handshake before we can attempt to
			// send a Data msg.
			synAck = PerformHandshake()
		})

		Describe("the happy path", func() {
			It("Data is built correctly", func() {
				payload := []byte{}
				dataMsg := SendDataWithPayload(payload)
				expectedPrevMessage := synAck

				By("Asserting the keysplitting message is correct")
				Expect(dataMsg.Type).To(Equal(ksmsg.Data))
				Expect(dataMsg.Signature).NotTo(BeEmpty())
				dataPayload, ok := dataMsg.KeysplittingPayload.(ksmsg.DataPayload)
				Expect(ok).To(BeTrue())

				By("Asserting the keysplitting message payload details are correct")
				Expect(dataPayload.SchemaVersion).To(Equal(agentSchemaVersion), "The schema version should match the agreed upon version found in the agent's SynAck")
				Expect(dataPayload.Type).To(BeEquivalentTo(ksmsg.Data))
				Expect(dataPayload.Action).To(Equal(testAction))
				Expect(dataPayload.TargetId).To(Equal(agentKeypair.Base64EncodedPublicKey))
				// Asserts that Validate() was called for the previous Ack sent by the
				// agent. If true, then this Data msg points to the correct message in
				// the chain
				Expect(dataPayload.HPointer).Should(Equal(expectedPrevMessage.Hash()), fmt.Sprintf("This Data msg's HPointer should point to the previously received message: %#v", expectedPrevMessage))
				Expect(dataPayload.ActionPayload).To(Equal(payload))
				expectedBzCertHash, ok := GetFakeBZCert().Hash()
				Expect(ok).Should(BeTrue(), "There should not be an error when hashing the expected BZCert")
				Expect(dataPayload.BZCertHash).To(Equal(expectedBzCertHash))
			})
		})

		Describe("failure modes", func() {
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
