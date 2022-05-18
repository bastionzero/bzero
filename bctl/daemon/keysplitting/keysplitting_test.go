package keysplitting

import (
	"testing"

	"bastionzero.com/bctl/v1/bctl/daemon/keysplitting/mocks"
	"bastionzero.com/bctl/v1/bctl/daemon/keysplitting/tokenrefresh"
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
	var err error

	// Setup SUT that is used by all tests
	BeforeEach(func() {
		// Setup keypairs to use for agent and daemon
		agentKeypair, err = tests.GenerateEd25519Key()
		Expect(err).ShouldNot(HaveOccurred())
		daemonKeypair, err = tests.GenerateEd25519Key()
		Expect(err).ShouldNot(HaveOccurred())

		// Setup mocks here
		mockTokenRefresher = &mocks.TokenRefresher{}

		// Init the SUT
		sut, err = New(logger.MockLogger(), agentKeypair.Base64EncodedPublicKey, mockTokenRefresher)
		Expect(err).ShouldNot(HaveOccurred())
	})

	AfterEach(func() {
		mockTokenRefresher.AssertExpectations(GinkgoT())
	})

	Describe("Validate SynAck messages", func() {
		var zliKsConfig *tokenrefresh.ZLIKeysplittingConfig

		BeforeEach(func() {
			// Permits all tests within this describe block to call BuildSyn()
			// without panic
			By("Mocking the token refresher to return a dummy KS config")
			zliKsConfig = &tokenrefresh.ZLIKeysplittingConfig{
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
			mockTokenRefresher.On("Refresh").Return(zliKsConfig, nil)
		})

		Context("When the SynAck is built on a previously sent SYN", func() {
			var synMsg *ksmsg.KeysplittingMessage
			var synAckMsg ksmsg.KeysplittingMessage
			BeforeEach(func() {
				By("Building a Syn without error")
				synMsg, err = sut.BuildSyn("test/action", []byte{}, true)
				Expect(err).ShouldNot(HaveOccurred())

				By("Pushing to the outbox")
				Expect(sut.Outbox()).Should(Receive(Equal(synMsg)))

				By("Building a SynAck without error")
				synAckMsg, err = synMsg.BuildUnsignedSynAck(
					[]byte{},
					agentKeypair.Base64EncodedPublicKey,
					util.Nonce(),
					ksmsg.SchemaVersion,
				)
				Expect(err).ShouldNot(HaveOccurred())
			})

			It("valid when the SynAck is signed", func() {
				By("Signing a SynAck without error")
				err = synAckMsg.Sign(agentKeypair.Base64EncodedPrivateKey)
				Expect(err).ShouldNot(HaveOccurred())

				By("Validating without error")
				err := sut.Validate(&synAckMsg)
				Expect(err).ShouldNot(HaveOccurred())
			})

			It("valid when the SynAck is signed by a legacy agent (CWC-1553)", func() {
				// Remove this test once CWC-1553 is addressed

				By("Create different agent keypair than the one used when signing SynAcks")
				diffAgentKeypair, err := tests.GenerateEd25519Key()
				Expect(err).ShouldNot(HaveOccurred())

				By("Signing a SynAck without error")
				err = synAckMsg.Sign(diffAgentKeypair.Base64EncodedPrivateKey)
				Expect(err).ShouldNot(HaveOccurred())

				// Modify the SUT for just this It node with a different agent
				// pubkey than the one set in SynAck's TargetPublicKey
				By("Modifying the SUT with this different agent keypair")
				sut.agentPubKey = diffAgentKeypair.Base64EncodedPublicKey
				Expect(err).ShouldNot(HaveOccurred())

				By("Validating without error")
				err = sut.Validate(&synAckMsg)
				Expect(err).ShouldNot(HaveOccurred())

			})

			It("invalid when the SynAck is unsigned", func() {
				err := sut.Validate(&synAckMsg)
				Expect(err).ShouldNot(BeNil())
			})
		})
	})
})
