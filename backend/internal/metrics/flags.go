package metrics

type FlagMark struct {
	TS    int64  `cbor:"1,keyasint" json:"ts"`
	Label string `cbor:"2,keyasint" json:"label"`
}

type FlagMarksRequest struct {
	BotID int64 `cbor:"1,keyasint"`
	From  int64 `cbor:"2,keyasint"`
}
