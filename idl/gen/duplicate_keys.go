package main

// Grant records discard values but still enforce the definition's decoded-key
// uniqueness policy. Preserve records already track their keys in Extras.
func tracksSkippedKeys(s *Definition, st Struct) bool {
	return st.UnknownFields == "grant" && s.Encoding.RefuseDuplicateKeys()
}
