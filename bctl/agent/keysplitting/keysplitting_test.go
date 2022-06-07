package keysplitting_test

import (
	"encoding/json"
	"testing"

	agentKs "bastionzero.com/bctl/v1/bctl/agent/keysplitting"
	"bastionzero.com/bctl/v1/bctl/agent/keysplitting/mocks"
	"bastionzero.com/bctl/v1/bzerolib/keysplitting/bzcert"
	ksmsg "bastionzero.com/bctl/v1/bzerolib/keysplitting/message"
	"bastionzero.com/bctl/v1/bzerolib/keysplitting/util"
	"bastionzero.com/bctl/v1/bzerolib/tests"

	"github.com/Masterminds/semver"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
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
			BZCert:        bzcert.BZCert{}, // TODO-Yuval: Fix this?
		}

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

		// Set schema version to use when building daemon+agent messages
		daemonSchemaVersion = ksmsg.SchemaVersion

		// Setup mocks here
		mockAgentKeysplittingConfig = &mocks.IKeysplittingConfig{}
		mockBzCertVerifier = &mocks.BZCertVerifier{}

		// Init the SUT
		sut, err = agentKs.New(agentKs.KeysplittingParameters{Config: mockAgentKeysplittingConfig, Verifier: mockBzCertVerifier})
		Expect(err).ShouldNot(HaveOccurred())
	})

	AfterEach(func() {
		mockAgentKeysplittingConfig.AssertExpectations(GinkgoT())
		mockBzCertVerifier.AssertExpectations(GinkgoT())
	})

	Describe("validate agent messages", func() {
		BuildData(nil)
		BuildSyn()
		sut.Validate(nil)
	})
})
