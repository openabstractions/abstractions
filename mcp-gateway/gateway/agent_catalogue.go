package gateway

// AgentBackend is the capability shape of the kept native agent helper.
// Optional capabilities remain selected by NewServer's ordinary assertions.
// The inert catalogue embeds this same shape; native helper parity tests catch
// additions or removals without maintaining a second list of public tool names.
type AgentBackend interface {
	MessagesReceiverBackend
	AppModelBackend
	enrollmentBackend
	APIReadBackend
	AsksBackend
	AsksWatchBackend
	MessagesWatchBackend
	MessagesAccessBackend
	MessagesPrepareBackend
	ModelsConnectBackend
	jobsFollowBackend
	ImageBackend
}
