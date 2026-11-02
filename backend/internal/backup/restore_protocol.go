package backup

type RestoreResult struct{ID string `cbor:"1,keyasint" json:"id"`;Bots int `cbor:"2,keyasint" json:"bots"`;Done bool `cbor:"3,keyasint" json:"done"`;Error string `cbor:"4,keyasint,omitempty" json:"error,omitempty"`}
