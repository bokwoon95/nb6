package main

import (
	"encoding/json"
	"fmt"
	"net/http"
)

func main() {
	http.ListenAndServe("127.0.0.1:6444", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Println(r.Host)
	}))
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
