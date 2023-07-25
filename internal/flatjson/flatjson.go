package flatjson

// TODO: package flatjson => flatjson.Unflatten(map[string]any) []byte => flatjson.Flatten([]byte) map[string]any
// "$.errors[''][{{ $i }}]" => "lorem ipsum"
// "$.username[{{ $i }}]" => "cannot be empty"

func Flatten(v any) map[string]any {
	return nil
}

func Unflatten(keyvalues map[string]any) any {
	return nil
}
