package main

import (
	"encoding/json"
	"fmt"
	"log"
)

func main() {
	var s bool
	err := json.Unmarshal([]byte(`true`), &s)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(s)
	b, err := json.Marshal(Bool(true))
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(string(b))
}

type Bool bool

func (b *Bool) UnmarshalJSON(data []byte) error {
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
		*b = Bool(isTrue)
	}
	return nil
}
