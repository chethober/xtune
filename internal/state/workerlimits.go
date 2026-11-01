package state

// WorkerExtra is another Worker, in another account, relaying to the same inbounds.
type WorkerExtra struct {
	Token   string `json:"token"` // that account's API token (Account: Workers Scripts:Edit, Account Analytics:Read)
	Account string `json:"account"`
	Name    string `json:"name"`
	Host    string `json:"host"` // NAME.SUBDOMAIN.workers.dev
}
