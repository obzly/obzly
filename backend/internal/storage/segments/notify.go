package segments

import "sync/atomic"

var uploadNotifier atomic.Pointer[func(*SegmentRecord)]

// SetUploadNotifier is told about every segment this process uploads
// (query.PublishUploaded: queriers pre-fetch what they will be asked for).
func SetUploadNotifier(notify func(*SegmentRecord)) {
	uploadNotifier.Store(&notify)
}

// NotifyUploaded reports record as uploaded under its ObjectKey.
func NotifyUploaded(record *SegmentRecord) {
	if notify := uploadNotifier.Load(); notify != nil {
		(*notify)(record)
	}
}
