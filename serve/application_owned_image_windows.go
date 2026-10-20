package main

func (h *heldApplicationManifest) ownedImage() ownedApplicationImage {
	if h == nil {
		return ownedApplicationImage{}
	}
	return ownedApplicationImage{program: h.program, code: h.Code, proof: h.Proof, pinned: h.Pinned, recheck: h.Recheck, close: h.Close}
}
