package controller

const controllerAgentName = "course-controller"
const PUBLIC_CLASSROOM_SUFFIX = "-public"
const TlsSecretName = "nchc-tls-secret"

const (
	// SuccessSynced is used as part of the Event 'reason' when a Foo is synced
	SuccessSynced = "Synced"
	// ErrResourceExists is used as part of the Event 'reason' when a Foo fails
	// to sync due to a Deployment of the same name already existing.
	ErrResourceExists = "ErrResourceExists"

	// MessageResourceExists is the message used for Events when a resource
	// fails to sync due to a Deployment already existing
	MessageResourceExists = "Resource %q already exists and is not managed by Course"
	// MessageResourceSynced is the message used for an Event fired when a Foo
	// is synced successfully
	MessageResourceSynced = "Course synced successfully"
)
