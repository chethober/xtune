package state

// AWGParams are the obfuscation settings server and clients share.
type AWGParams struct {
	Jc     int    `json:"jc"`
	Jmin   int    `json:"jmin"`
	Jmax   int    `json:"jmax"`
	S1     int    `json:"s1"`
	S2     int    `json:"s2"`
	H1     uint32 `json:"h1"`
	H2     uint32 `json:"h2"`
	H3     uint32 `json:"h3"`
	H4     uint32 `json:"h4"`
	Priv   string `json:"privateKey"` // server key (base64)
	Public string `json:"publicKey"`
}
