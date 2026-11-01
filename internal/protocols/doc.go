// Package protocols turns xtune's state into what servers and apps run: the Xray server config,
// client outbounds and the failover config, share links and subscriptions (with their names and
// flags), and the configs and profiles of the WireGuard, AmneziaWG, SSH and DNS tunnels, MTProto,
// OpenVPN and IKEv2. It only builds; deploying and measuring live in the main package.
package protocols
