package main

import (
	bzplugin "bastionzero.com/bctl/v1/bzerolib/plugin"
)

const (
	// general-purpose configuration
	SESSION_ID                    = "SESSION_ID"                    // Session id from Zli
	SESSION_TOKEN                 = "SESSION_TOKEN"                 // Session token from Zli
	AUTH_HEADER                   = "AUTH_HEADER"                   // Auth header from Zli
	CONNECTION_ID                 = "CONNECTION_ID"                 // The bzero connection id for the shell connection
	CONNECTION_SERVICE_URL        = "CONNECTION_SERVICE_URL"        // URL of connection service
	CONNECTION_SERVICE_AUTH_TOKEN = "CONNECTION_SERVICE_AUTH_TOKEN" // The auth token returned from the universal connection service
	SERVICE_URL                   = "SERVICE_URL"                   // URL of bastion
	TARGET_ID                     = "TARGET_ID"                     // Id of the target to connect to
	PLUGIN                        = "PLUGIN"                        // Plugin to activate
	AGENT_PUB_KEY                 = "AGENT_PUB_KEY"                 // Base64 encoded string of agent's public key

	// for interacting with the user and the ZLI
	LOCAL_PORT            = "LOCAL_PORT"            // Daemon port To Use
	LOCAL_HOST            = "LOCAL_HOST"            // Daemon host To Use
	CONFIG_PATH           = "CONFIG_PATH"           // Local storage path to zli config
	LOG_PATH              = "LOG_PATH"              // Path to log file for daemon
	REFRESH_TOKEN_COMMAND = "REFRESH_TOKEN_COMMAND" // zli constructed command for refreshing id tokens

	// variables used by multiple plugins
	REMOTE_PORT = "REMOTE_PORT" // Remote target port to connect to
	REMOTE_HOST = "REMOTE_HOST" // Remote target host to connect to
	TARGET_USER = "TARGET_USER" // OS user or Kube role to assume

	// kube plugin variables
	TARGET_GROUPS   = "TARGET_GROUPS"   // Comma-separated list of Kube groups to assume
	LOCALHOST_TOKEN = "LOCALHOST_TOKEN" // token to validate kube commands
	CERT_PATH       = "CERT_PATH"       // Path to cert to use for our localhost server
	KEY_PATH        = "KEY_PATH"        // Path to key to use for our localhost server

	// shell plugin variables
	DATACHANNEL_ID = "DATACHANNEL_ID" // The datachannel id to attach to an existing shell connection

	// ssh plugin variables
	IDENTITY_FILE    = "IDENTITY_FILE"    // Path to an SSH IdentityFile
	KNOWN_HOSTS_FILE = "KNOWN_HOSTS_FILE" // Path to bastionzero-known_hosts
	SSH_ACTION       = "SSH_ACTION"       // One of ['opaque', 'transparent']
	HOSTNAMES        = "HOSTNAMES"        // Comma-separated list of hostNames to use for this target
)

var (
	requriedGlobalVars = []string{CONNECTION_ID, CONNECTION_SERVICE_URL, CONNECTION_SERVICE_AUTH_TOKEN, SESSION_ID, SESSION_TOKEN, AUTH_HEADER, LOG_PATH, CONFIG_PATH, AGENT_PUB_KEY, REFRESH_TOKEN_COMMAND}

	requriedPluginVars = map[bzplugin.PluginName][]string{
		bzplugin.Kube:  {LOCAL_PORT, TARGET_USER, TARGET_ID, LOCALHOST_TOKEN, CERT_PATH, KEY_PATH},
		bzplugin.Db:    {LOCAL_PORT, REMOTE_HOST, REMOTE_PORT},
		bzplugin.Web:   {LOCAL_PORT, REMOTE_HOST, REMOTE_PORT},
		bzplugin.Shell: {TARGET_USER, CONNECTION_ID},
		bzplugin.Ssh:   {TARGET_USER, TARGET_ID, REMOTE_HOST, REMOTE_PORT, IDENTITY_FILE, KNOWN_HOSTS_FILE, HOSTNAMES, SSH_ACTION},
	}
)

type EnvVar struct {
	Value string
	Seen  bool
}

// environment variable management dictionary
// maps the name of an env var to its value and whether or not it is set
var config = map[string]EnvVar{
	// general-purpose configuration
	SESSION_ID:                    {},
	SESSION_TOKEN:                 {},
	AUTH_HEADER:                   {},
	CONNECTION_ID:                 {},
	CONNECTION_SERVICE_URL:        {},
	CONNECTION_SERVICE_AUTH_TOKEN: {},
	SERVICE_URL:                   {Value: prodServiceUrl},
	TARGET_ID:                     {},
	PLUGIN:                        {},
	AGENT_PUB_KEY:                 {},

	// for interacting with the user and the ZLI
	LOCAL_PORT:            {},
	LOCAL_HOST:            {},
	CONFIG_PATH:           {},
	LOG_PATH:              {},
	REFRESH_TOKEN_COMMAND: {},

	// variables used by multiple plugins
	REMOTE_PORT: {Value: "-1"},
	REMOTE_HOST: {},
	TARGET_USER: {},

	// kube plugin variables
	TARGET_GROUPS:   {},
	LOCALHOST_TOKEN: {},
	CERT_PATH:       {},
	KEY_PATH:        {},

	// shell plugin variables
	DATACHANNEL_ID: {},

	// ssh plugin variables
	IDENTITY_FILE:    {},
	KNOWN_HOSTS_FILE: {},
	SSH_ACTION:       {},
	HOSTNAMES:        {},
}
