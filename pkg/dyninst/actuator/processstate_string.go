// Code generated by "stringer -type=processState -trimprefix=processState"; DO NOT EDIT.

package actuator

import "strconv"

func _() {
	// An "invalid array index" compiler error signifies that the constant values have changed.
	// Re-run the stringer command to generate them again.
	var x [1]struct{}
	_ = x[processStateInvalid-0]
	_ = x[processStateWaitingForProgram-1]
	_ = x[processStateAttaching-2]
	_ = x[processStateAttached-3]
	_ = x[processStateDetaching-4]
	_ = x[processStateLoadingFailed-5]
}

const _processState_name = "InvalidWaitingForProgramAttachingAttachedDetachingLoadingFailed"

var _processState_index = [...]uint8{0, 7, 24, 33, 41, 50, 63}

func (i processState) String() string {
	if i >= processState(len(_processState_index)-1) {
		return "processState(" + strconv.FormatInt(int64(i), 10) + ")"
	}
	return _processState_name[_processState_index[i]:_processState_index[i+1]]
}
