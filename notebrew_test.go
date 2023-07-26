package nb6

import (
	"encoding/json"
)

// Boolean is a bool that additionally supports unmarshalling from JSON
// strings.
type Boolean bool

func (b *Boolean) UnmarshalJSON(data []byte) error {
	switch string(data) {
	case `1`, `"1"`, `"t"`, `"T"`, `"TRUE"`, `"true"`, `"True"`:
		*b = true
	case `0`, `"0"`, `"f"`, `"F"`, `"FALSE"`, `"false"`, `"False"`:
		*b = false
	default:
		var isTrue bool
		err := json.Unmarshal(data, &isTrue)
		if err != nil {
			return err
		}
		*b = Boolean(isTrue)
	}
	return nil
}
