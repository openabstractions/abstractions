package setup

// AgentEnrollmentCapture is private Setup recovery metadata. Native Applications
// rereads every fact before removal; this record supplies no authority.
type AgentEnrollmentCapture struct {
	Application string `json:"application"`
	Program     string `json:"program"`
	FileKey     string `json:"file_key"`
	Revision    string `json:"revision"`
}
