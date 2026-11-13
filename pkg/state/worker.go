package state

// Worker is the deployment's Cloudflare Worker.
type Worker struct {
	Account string            `json:"account"`
	Name    string            `json:"name"`
	Host    string            `json:"host"`             // NAME.SUBDOMAIN.workers.dev
	Domain  string            `json:"domain,omitempty"` // a hostname on one of the user's zones
	Routes  map[string]string `json:"routes,omitempty"` // path -> origin IP:port, as last uploaded
	Extra   []*WorkerExtra    `json:"extra,omitempty"`  // more Workers, in other accounts, on the same routes
	Limit   int64             `json:"limit,omitempty"`  // requests a day for the usage alerts: 0 = the free plan's, -1 = no alerts
}

// WorkerUse is one Worker's request count today, as Cloudflare's analytics report it.
type WorkerUse struct {
	Host     string `json:"host"`
	Requests int64  `json:"requests"`
}
