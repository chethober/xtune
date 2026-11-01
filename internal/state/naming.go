package state

// Naming is how client apps show this server's configs and subscriptions. Every field is
// optional: the zero value names configs "USER-ID" and subscriptions "xtune - USER", with the
// flag of the server's country in front once it is known.
type Naming struct {
	Label string `json:"label,omitempty"` // this server's name, {server} in the templates ("Frankfurt")
	// Flag is the ISO 3166-1 alpha-2 code of the country traffic leaves from: the server's own
	// location. "" = not known yet (looked up automatically), "off" = no flag.
	Flag     string `json:"flag,omitempty"`
	Detected bool   `json:"detected,omitempty"` // Flag came from a lookup, so a move to a new IP looks it up again
	Checked  int64  `json:"checked,omitempty"`  // unix seconds of the last failed lookup, to retry daily
	Config   string `json:"config,omitempty"`   // config name template, "" = DefaultConfigName
	Sub      string `json:"sub,omitempty"`      // subscription name template, "" = DefaultSubName

	Via string `json:"-"` // the relay these configs go through: {via}, set on the relay's view only
}

// Placeholders in the templates: {flag} {country} {server} {user} {id} {proto} {port}, and {via}
// for the relay a config goes through. Parts left empty are dropped with their separators.
const (
	DefaultConfigName = "{flag} {user}-{id}"
	DefaultSubName    = "{flag} xtune - {user}"
)
