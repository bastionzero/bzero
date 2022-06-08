package keysplitting_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	agentKs "bastionzero.com/bctl/v1/bctl/agent/keysplitting"
	"bastionzero.com/bctl/v1/bctl/agent/keysplitting/mocks"
	"bastionzero.com/bctl/v1/bzerolib/keysplitting/bzcert"
	ksmsg "bastionzero.com/bctl/v1/bzerolib/keysplitting/message"
	"bastionzero.com/bctl/v1/bzerolib/keysplitting/util"
	"bastionzero.com/bctl/v1/bzerolib/tests"

	"github.com/Masterminds/semver"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/stretchr/testify/mock"
)

func TestAgentKeysplitting(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Agent keysplitting suite")
}

var _ = Describe("Agent keysplitting", func() {
	var sut *agentKs.Keysplitting

	var agentKeypair *tests.Ed25519KeyPair
	var daemonKeypair *tests.Ed25519KeyPair

	var mockAgentKeysplittingConfig *mocks.IKeysplittingConfig
	var mockBzCertVerifier *mocks.BZCertVerifier

	var daemonSchemaVersion string
	var bzCertHash string
	var bzCertExpirationTime time.Time
	const testAction string = "test/action"

	GetDaemonSchemaVersionAsSemVer := func() *semver.Version {
		parsedSchemaVersion, err := semver.NewVersion(daemonSchemaVersion)
		Expect(err).ShouldNot(HaveOccurred())
		return parsedSchemaVersion
	}

	// // Build the BZero Certificate then store hash for future messages
	// bzCert, err := k.buildBZCert()
	// if err != nil {
	// 	return nil, fmt.Errorf("error building bzecert: %w", err)
	// } else {
	// 	if hash, ok := bzCert.Hash(); ok {
	// 		k.bzcertHash = hash
	// 	} else {
	// 		return nil, fmt.Errorf("could not hash BZ Certificate")
	// 	}
	// }

	// Helper build daemon message funcs
	BuildSynWithPayload := func(payload []byte) *ksmsg.KeysplittingMessage {
		// TODO-Yuval: See if I can remove this
		payloadBytes, err := json.Marshal(payload)
		Expect(err).ShouldNot(HaveOccurred())

		// Build the keysplitting message
		synPayload := ksmsg.SynPayload{
			SchemaVersion: GetDaemonSchemaVersionAsSemVer().String(),
			Type:          string(ksmsg.Syn),
			Action:        testAction,
			ActionPayload: payloadBytes, // payload
			TargetId:      agentKeypair.Base64EncodedPublicKey,
			Nonce:         util.Nonce(),
			// We mock the BZCertVerifier elsewhere. We only need to set
			// ClientPublicKey because that is the only field the agent uses
			// after the verifier successfully validates.
			BZCert: bzcert.BZCert{
				ClientPublicKey: daemonKeypair.Base64EncodedPublicKey,
			},
		}

		// Set bzCertHash variable, so Data messages can reference it
		var ok bool
		bzCertHash, ok = synPayload.BZCert.Hash()
		Expect(ok).Should(BeTrue(), "There should not be an error when hashing the fake BZCert")

		return &ksmsg.KeysplittingMessage{
			Type:                ksmsg.Syn,
			KeysplittingPayload: synPayload,
		}
	}
	BuildSyn := func() *ksmsg.KeysplittingMessage {
		return BuildSynWithPayload([]byte{})
	}
	BuildDataWithPayload := func(ackMsg *ksmsg.KeysplittingMessage, payload []byte) *ksmsg.KeysplittingMessage {
		dataMsg, err := ackMsg.BuildUnsignedData(
			testAction,
			payload,
			bzCertHash,
			GetDaemonSchemaVersionAsSemVer().String(),
		)
		Expect(err).ShouldNot(HaveOccurred())
		return &dataMsg
	}
	BuildData := func(ackMsg *ksmsg.KeysplittingMessage) *ksmsg.KeysplittingMessage {
		return BuildDataWithPayload(ackMsg, []byte{})
	}
	SignDaemonMsg := func(daemonMsg *ksmsg.KeysplittingMessage) {
		err := daemonMsg.Sign(daemonKeypair.Base64EncodedPrivateKey)
		Expect(err).ShouldNot(HaveOccurred())
	}
	// Use this helper method to quickly validate messages, so another message
	// can be received. Please prefer to call Validate() directly (and not use
	// this function) when the It() is explictly asserting validation.
	ValidateDaemonMsg := func(daemonMsg *ksmsg.KeysplittingMessage) {
		err := sut.Validate(daemonMsg)
		Expect(err).ShouldNot(HaveOccurred())
	}

	// Setup SUT that is used by all tests
	BeforeEach(func() {
		// Setup keypairs to use for agent and daemon
		var err error
		agentKeypair, err = tests.GenerateEd25519Key()
		GinkgoWriter.Printf("Agent keypair: Private key: %v; Public key: %v\n", agentKeypair.Base64EncodedPrivateKey, agentKeypair.Base64EncodedPublicKey)
		Expect(err).ShouldNot(HaveOccurred())
		daemonKeypair, err = tests.GenerateEd25519Key()
		GinkgoWriter.Printf("Daemon keypair: Private key: %v; Public key: %v\n", daemonKeypair.Base64EncodedPrivateKey, daemonKeypair.Base64EncodedPublicKey)
		Expect(err).ShouldNot(HaveOccurred())

		// Set BZCert expiration time to the future
		bzCertExpirationTime = time.Now().Add(1 * time.Hour)

		// Set schema version to use when building daemon+agent messages
		daemonSchemaVersion = ksmsg.SchemaVersion

		// Setup mocks here
		mockAgentKeysplittingConfig = &mocks.IKeysplittingConfig{}
		mockBzCertVerifier = &mocks.BZCertVerifier{}

		// Configure default behavior for mocks here. An individual test (or
		// context) can clear these by setting mock.ExpectedCalls to nil
		mockAgentKeysplittingConfig.On("GetPublicKey").Return(agentKeypair.Base64EncodedPublicKey)
		mockAgentKeysplittingConfig.On("GetPrivateKey").Return(agentKeypair.Base64EncodedPrivateKey)
	})

	AfterEach(func() {
		mockAgentKeysplittingConfig.AssertExpectations(GinkgoT())
		mockBzCertVerifier.AssertExpectations(GinkgoT())
	})

	Describe("validate daemon messages", func() {
		var msgUnderTest *ksmsg.KeysplittingMessage

		AssertBehavior := func() {
			It("validate succeeds when the message is signed", func() {
				By(fmt.Sprintf("Signing %v without error", msgUnderTest.Type))
				SignDaemonMsg(msgUnderTest)

				By("Validating without error")
				err := sut.Validate(msgUnderTest)
				Expect(err).ShouldNot(HaveOccurred())
			})
		}

		BeforeEach(func() {
			// Init the SUT
			var err error
			sut, err = agentKs.New(agentKs.KeysplittingParameters{Config: mockAgentKeysplittingConfig, Verifier: mockBzCertVerifier})
			Expect(err).ShouldNot(HaveOccurred())
		})

		Context("when the message is the wrong type", func() {
			var validateError error

			AssertFailedBehavior := func() {
				It("errors", func() {
					Expect(validateError).Should(HaveOccurred())
				})
			}

			JustBeforeEach(func() {
				validateError = sut.Validate(msgUnderTest)
			})

			Context("when the message is a SynAck", func() {
				BeforeEach(func() {
					msgUnderTest = &ksmsg.KeysplittingMessage{Type: ksmsg.SynAck}
				})

				AssertFailedBehavior()
			})

			Context("when the message is a DataAck", func() {
				BeforeEach(func() {
					msgUnderTest = &ksmsg.KeysplittingMessage{Type: ksmsg.DataAck}
				})

				AssertFailedBehavior()
			})
		})

		Context("when the message is a Data-->SynAck-->Syn", func() {
			var synMsg *ksmsg.KeysplittingMessage

			BeforeEachBehavior := func() {
				// We must build a Syn, validate it, and build a SynAck, so that
				// we can send Data successfully
				By("Building a Syn message without error")
				synMsg = BuildSyn()
				By("Signing Syn message without error")
				SignDaemonMsg(synMsg)
				// Mock the BZCertVerifier so that Verify succeeds on our Syn's
				// BZCert
				mockBzCertVerifier.On("Verify", synMsg.KeysplittingPayload.(ksmsg.SynPayload).BZCert).Return(bzCertHash, bzCertExpirationTime, nil)
				ValidateDaemonMsg(synMsg)
				// Sets expected HPointer which our Data must set correctly in
				// order to validate
				synAck, err := sut.BuildAck(synMsg, testAction, []byte{})
				Expect(err).ShouldNot(HaveOccurred(), "because we should be able to build a SynAck from a valid Syn message")

				msgUnderTest = BuildData(&synAck)
			}

			Describe("the happy path", func() {
				BeforeEach(func() {
					BeforeEachBehavior()
					// There is nothing extra to configure
				})

				AssertBehavior()
			})

			Describe("failure modes", func() {
				var validateError error

				AssertFailedBehavior := func() {
					It("SynAck nonce should not refer to invalid Data message", func() {
						By("Building SynAck without error")
						synAck, err := sut.BuildAck(synMsg, testAction, []byte{})
						Expect(err).ShouldNot(HaveOccurred())

						invalidDataMsgHPointer, err := msgUnderTest.GetHpointer()
						Expect(err).ShouldNot(HaveOccurred())

						Expect(synAck.KeysplittingPayload.(ksmsg.SynAckPayload).Nonce).ShouldNot(Equal(invalidDataMsgHPointer), "because if the Data message failed to validate, the SynAck should not build off an invalid Data message")
					})
				}

				JustBeforeEach(func() {
					validateError = sut.Validate(msgUnderTest)
				})

				Context("when the BZCert hash does not match the agent's stored BZCert hash", func() {
					BeforeEach(func() {
						BeforeEachBehavior()

						By("Modifying BZCert hash not to match")
						dataPayload, _ := msgUnderTest.KeysplittingPayload.(ksmsg.DataPayload)
						dataPayload.BZCertHash = "does not match"
						msgUnderTest.KeysplittingPayload = dataPayload

						By(fmt.Sprintf("Signing %v without error", msgUnderTest.Type))
						SignDaemonMsg(msgUnderTest)
					})

					AssertFailedBehavior()

					It("errors", func() {
						Expect(validateError).Should(MatchError(agentKs.ErrBZCertMismatch))
					})
				})

				Context("when the message is unsigned", func() {
					BeforeEach(func() {
						BeforeEachBehavior()
						msgUnderTest.Signature = ""
					})

					AssertFailedBehavior()

					It("errors", func() {
						Expect(validateError).Should(MatchError(agentKs.ErrInvalidSignature))
					})
				})

				Context("when the BZCert has expired", func() {
					BeforeEach(func() {
						// Set expiration time to the past
						bzCertExpirationTime = time.Now().Add(-1 * time.Hour)
						BeforeEachBehavior()

						By(fmt.Sprintf("Signing %v without error", msgUnderTest.Type))
						SignDaemonMsg(msgUnderTest)
					})

					AssertFailedBehavior()

					It("errors", func() {
						Expect(validateError).Should(MatchError(agentKs.ErrBZCertExpired))
					})
				})

				Context("when the HPointer points to the wrong message", func() {
					BeforeEach(func() {
						BeforeEachBehavior()

						By("Modifying HPointer to point to the wrong message")
						dataPayload, _ := msgUnderTest.KeysplittingPayload.(ksmsg.DataPayload)
						dataPayload.HPointer = "wrong message hash"
						msgUnderTest.KeysplittingPayload = dataPayload

						By(fmt.Sprintf("Signing %v without error", msgUnderTest.Type))
						SignDaemonMsg(msgUnderTest)
					})

					AssertFailedBehavior()

					It("errors", func() {
						Expect(validateError).Should(MatchError(agentKs.ErrUnexpectedHPointer))
					})
				})
			})
		})

		Context("when the message is a Syn", func() {
			BeforeEach(func() {
				By("Building a Syn message without error")
				msgUnderTest = BuildSyn()
				// Mock the BZCertVerifier so that Verify succeeds on our Syn's
				// BZCert
				mockBzCertVerifier.On("Verify", msgUnderTest.KeysplittingPayload.(ksmsg.SynPayload).BZCert).Return(bzCertHash, bzCertExpirationTime, nil)
			})

			Describe("the happy path", func() {
				// There is nothing extra to setup
				AssertBehavior()

				// Remove this test once CWC-1553 is addressed
				It("validate succeeds when the message is signed by a legacy daemon (CWC-1553)", func() {
					By("Modifying schema version to be invalid")
					synPayload, _ := msgUnderTest.KeysplittingPayload.(ksmsg.SynPayload)
					// Change schema version to version prior to targetId check
					synPayload.SchemaVersion = "1.0"
					synPayload.TargetId = "does not match"
					msgUnderTest.KeysplittingPayload = synPayload

					By(fmt.Sprintf("Signing %v without error", msgUnderTest.Type))
					SignDaemonMsg(msgUnderTest)

					By("Validating without error")
					err := sut.Validate(msgUnderTest)
					Expect(err).ShouldNot(HaveOccurred())
				})
			})

			Describe("failure modes", func() {
				var validateError error

				AssertFailedBehavior := func() {
					It("cannot validate Data messages", func() {
						By("Building SynAck so that we can build a Data message")
						synAck, err := msgUnderTest.BuildUnsignedSynAck([]byte{}, agentKeypair.Base64EncodedPublicKey, util.Nonce(), ksmsg.SchemaVersion)
						Expect(err).ShouldNot(HaveOccurred())
						dataMsg := BuildData(&synAck)

						err = sut.Validate(dataMsg)
						Expect(err).Should(HaveOccurred(), "because if the Syn failed to validate, then the agent should refuse to accept Data messages as the handshake never completed")
					})
				}

				JustBeforeEach(func() {
					validateError = sut.Validate(msgUnderTest)
				})

				Context("when the BZCert is invalid", func() {
					var bzCertVerifierError error

					BeforeEach(func() {
						// Reset the mock for this context because it already
						// has an expected call defined in an outer context
						mockBzCertVerifier.ExpectedCalls = nil
						bzCertVerifierError = errors.New("BZCert error")
						mockBzCertVerifier.On("Verify", mock.Anything).Return("", time.Time{}, bzCertVerifierError)
					})

					AssertFailedBehavior()

					It("errors", func() {
						Expect(validateError).Should(MatchError(bzCertVerifierError))
					})
				})

				Context("when the message is unsigned", func() {
					BeforeEach(func() {
						msgUnderTest.Signature = ""
					})

					AssertFailedBehavior()

					It("errors", func() {
						Expect(validateError).Should(MatchError(agentKs.ErrInvalidSignature))
					})
				})

				Context("when the schema version cannot be parsed", func() {
					BeforeEach(func() {
						By("Modifying schema version to be invalid")
						synPayload, _ := msgUnderTest.KeysplittingPayload.(ksmsg.SynPayload)
						synPayload.SchemaVersion = "bad-version"
						msgUnderTest.KeysplittingPayload = synPayload

						By(fmt.Sprintf("Signing %v without error", msgUnderTest.Type))
						SignDaemonMsg(msgUnderTest)
					})

					AssertFailedBehavior()

					It("errors", func() {
						Expect(validateError).Should(MatchError(agentKs.ErrFailedToParseVersion))
					})
				})

				Context("when the target ID does not match the agent's public key", func() {
					BeforeEach(func() {
						By("Modifying target ID to not match the agent's public key")
						synPayload, _ := msgUnderTest.KeysplittingPayload.(ksmsg.SynPayload)
						synPayload.TargetId = "does not match"
						msgUnderTest.KeysplittingPayload = synPayload

						By(fmt.Sprintf("Signing %v without error", msgUnderTest.Type))
						SignDaemonMsg(msgUnderTest)
					})

					AssertFailedBehavior()

					It("errors", func() {
						Expect(validateError).Should(MatchError(agentKs.ErrTargetIdMismatch))
					})
				})
			})
		})
	})
})
