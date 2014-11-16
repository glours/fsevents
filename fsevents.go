package fsevents

/*
#cgo LDFLAGS: -framework CoreServices
#include <CoreServices/CoreServices.h>
#include <sys/stat.h>

static CFArrayRef ArrayCreateMutable(int len) {
	return CFArrayCreateMutable(NULL, len, &kCFTypeArrayCallBacks);
}

extern void fsevtCallback(FSEventStreamRef p0, void * info, size_t p1, char** p2, FSEventStreamEventFlags* p3, FSEventStreamEventId* p4);

static FSEventStreamRef EventStreamCreateRelativeToDevice(FSEventStreamContext * context, dev_t dev, CFArrayRef paths, FSEventStreamEventId since, CFTimeInterval latency, FSEventStreamCreateFlags flags) {
	return FSEventStreamCreateRelativeToDevice(NULL, (FSEventStreamCallback) fsevtCallback, context, dev, paths, since, latency, flags);
}

static FSEventStreamRef EventStreamCreate(FSEventStreamContext * context, CFArrayRef paths, FSEventStreamEventId since, CFTimeInterval latency, FSEventStreamCreateFlags flags) {
	return FSEventStreamCreate(NULL, (FSEventStreamCallback) fsevtCallback, context, paths, since, latency, flags);
}
*/
import "C"
import "unsafe"
import "path/filepath"
import "time"

const EventIdSinceNow = uint64(C.kFSEventStreamEventIdSinceNow + (1 << 64))

// CreateFlags for creating a New stream.
type CreateFlags uint32

// kFSEventStreamCreateFlag...
const (
	// use CoreFoundation types instead of raw C types (disabled)
	useCFTypes CreateFlags = 1 << iota

	// NoDefer sends events on the leading edge (for interactive applications).
	// By default events are delivered after latency seconds (for background tasks).
	NoDefer

	// WatchRoot for a change to occur to a directory along the path being watched.
	WatchRoot

	// IgnoreSelf doesn't send events triggered by the current process (OS X 10.6+).
	IgnoreSelf

	// FileEvents sends events about individual files, generating significantly
	// more events (OS X 10.7+) than directory level notifications.
	FileEvents
)

// EventFlags passed to the FSEventStreamCallback function.
type EventFlags uint32

// kFSEventStreamEventFlag...
const (
	// MustScanSubDirs indicates that events were coalesced hierarchically.
	MustScanSubDirs EventFlags = 1 << iota
	// UserDropped or KernelDropped is set alongside MustScanSubDirs
	// to help diagnose the problem.
	UserDropped
	KernelDropped

	// EventIdsWrapped indicates the 64-bit event ID counter wrapped around.
	EventIdsWrapped

	// HistoryDone is a sentinel event when retrieving events sinceWhen.
	HistoryDone

	// RootChanged indicates a change to a directory along the path being watched.
	RootChanged

	// Mount for a volume mounted underneath the path being monitored.
	Mount
	// Unmount event occurs after a volume is unmounted.
	Unmount

	// The following flags are only set when using FileEvents.

	ItemCreated
	ItemRemoved
	ItemInodeMetaMod
	ItemRenamed
	ItemModified
	ItemFinderInfoMod
	ItemChangeOwner
	ItemXattrMod
	ItemIsFile
	ItemIsDir
	ItemIsSymlink
)

type Event struct {
	Path  string
	Flags EventFlags
	Id    uint64
}

//export fsevtCallback
func fsevtCallback(stream C.FSEventStreamRef, info unsafe.Pointer, numEvents C.size_t, paths **C.char, flags *C.FSEventStreamEventFlags, ids *C.FSEventStreamEventId) {
	events := make([]Event, int(numEvents))

	es := (*EventStream)(info)

	for i := 0; i < int(numEvents); i++ {
		cpaths := uintptr(unsafe.Pointer(paths)) + (uintptr(i) * unsafe.Sizeof(*paths))
		cpath := *(**C.char)(unsafe.Pointer(cpaths))

		cflags := uintptr(unsafe.Pointer(flags)) + (uintptr(i) * unsafe.Sizeof(*flags))
		cflag := *(*C.FSEventStreamEventFlags)(unsafe.Pointer(cflags))

		cids := uintptr(unsafe.Pointer(ids)) + (uintptr(i) * unsafe.Sizeof(*ids))
		cid := *(*C.FSEventStreamEventId)(unsafe.Pointer(cids))

		events[i] = Event{Path: C.GoString(cpath), Flags: EventFlags(cflag), Id: uint64(cid)}
		// Record the latest EventId to support resuming the stream
		es.EventId = uint64(cid)
	}

	es.Events <- events
}

func FSEventsLatestId() uint64 {
	return uint64(C.FSEventsGetCurrentEventId())
}

func DeviceForPath(pth string) int64 {
	cStat := C.struct_stat{}
	cPath := C.CString(pth)
	defer C.free(unsafe.Pointer(cPath))

	_ = C.lstat(cPath, &cStat)
	return int64(cStat.st_dev)
}

func GetIdForDeviceBeforeTime(dev, tm int64) uint64 {
	return uint64(C.FSEventsGetLastEventIdForDeviceBeforeTime(C.dev_t(dev), C.CFAbsoluteTime(tm)))
}

/*

	Primary EventStream interface.
	You can provide your own event channel if you wish (or one will be created
	on Start).

	es := &EventStream{Paths: []string{"/tmp"}, Flags: 0}
	es.Start()
	es.Stop()

*/

type EventStream struct {
	stream C.FSEventStreamRef
	rlref  C.CFRunLoopRef

	Events  chan []Event
	Paths   []string
	Flags   CreateFlags
	EventId uint64
	Resume  bool
	Latency time.Duration
	Device  int64
}

func (es *EventStream) Start() {
	cPaths := C.ArrayCreateMutable(C.int(len(es.Paths)))
	defer C.CFRelease(C.CFTypeRef(cPaths))

	for _, p := range es.Paths {
		p, _ = filepath.Abs(p)
		cpath := C.CString(p)
		defer C.free(unsafe.Pointer(cpath))

		str := C.CFStringCreateWithCString(nil, cpath, C.kCFStringEncodingUTF8)
		C.CFArrayAppendValue(cPaths, unsafe.Pointer(str))
	}

	since := C.FSEventStreamEventId(EventIdSinceNow)
	if es.Resume {
		since = C.FSEventStreamEventId(es.EventId)
	}

	if es.Events == nil {
		es.Events = make(chan []Event)
	}

	context := C.FSEventStreamContext{info: unsafe.Pointer(es)}
	latency := C.CFTimeInterval(float64(es.Latency) / float64(time.Second))
	if es.Device != 0 {
		es.stream = C.EventStreamCreateRelativeToDevice(&context, C.dev_t(es.Device), cPaths, since, latency, C.FSEventStreamCreateFlags(es.Flags))
	} else {
		es.stream = C.EventStreamCreate(&context, cPaths, since, latency, C.FSEventStreamCreateFlags(es.Flags))
	}

	go func() {
		es.rlref = C.CFRunLoopGetCurrent()
		C.FSEventStreamScheduleWithRunLoop(es.stream, es.rlref, C.kCFRunLoopDefaultMode)
		C.FSEventStreamStart(es.stream)
		C.CFRunLoopRun()
	}()
}

func (es *EventStream) Flush(sync bool) {
	if sync {
		C.FSEventStreamFlushSync(es.stream)
	} else {
		C.FSEventStreamFlushAsync(es.stream)
	}
}

func (es *EventStream) Stop() {
	C.FSEventStreamStop(es.stream)
	C.FSEventStreamInvalidate(es.stream)
	C.FSEventStreamRelease(es.stream)
	C.CFRunLoopStop(es.rlref)
}

func (es *EventStream) Restart() {
	es.Stop()
	es.Resume = true
	es.Start()
}
