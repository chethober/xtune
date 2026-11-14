package state

// BackupPlan is the schedule and where the backups go.
type BackupPlan struct {
	Every    int64     `json:"every"`              // seconds between backups
	Pass     string    `json:"pass"`               // passphrase the backups are sealed with
	Telegram bool      `json:"telegram,omitempty"` // send to the alert chat (xtune notify telegram)
	S3       *S3Target `json:"s3,omitempty"`
}

// S3Target is a bucket on an S3-compatible service, addressed path-style.
type S3Target struct {
	Endpoint  string `json:"endpoint"` // https://ACCOUNT.r2.cloudflarestorage.com
	Region    string `json:"region"`   // "auto" for R2
	Bucket    string `json:"bucket"`
	Prefix    string `json:"prefix,omitempty"`
	AccessKey string `json:"accessKey"`
	SecretKey string `json:"secretKey"`
}
