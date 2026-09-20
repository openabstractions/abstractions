package main

import "testing"

func TestInternalRefusalMappingsRejectUnknownWords(t *testing.T) {
	if inferenceListRefusal("invented") != 0 || inferenceEditRefusal("invented") != 0 || inferenceAuditRefusal("invented") != 0 || declarationListRefusal("invented") != 0 || declarationEditRefusal("invented") != 0 {
		t.Fatal("unknown internal refusal mapped to a valid wire outcome")
	}
}
