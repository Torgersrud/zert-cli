package main

// The copy arms' shared prologue — the half of `zert <path>` and
// `zert extract` that is one conversation, not two. Both arms name the
// machine the copy streams through the same way, because the
// conversation with the door is the door module's (ADR-0001) and a
// prologue that drifted would be the drift that module exists to
// prevent. What each arm keeps is genuinely its own: the name it
// validates, the remote command it spells, its codec and its wording.

import "io"

// resolveCopyTarget answers the question both copy arms open with:
// which machine does this copy stream through, and did this call place
// it. Login, the profile lookup (concurrent with the door's listing —
// the two asks are independent), the which-row ask, the
// several-machine refusal, the pubkey bootstrap and — when the door
// named no running machine — the machine ask with its story are the
// prologue's, once. stderr is the arm's error writer: the door's story
// and any refusal's way forward land there.
func resolveCopyTarget(stderr io.Writer) (creds *Credentials, vm *vmRow, created bool, err error) {
	creds, a := requireLogin()
	var me *meInfo
	meCh := make(chan error, 1)
	go func() {
		var err error
		me, err = getMe(a)
		meCh <- err
	}()
	which, row, err := askWhichRow(a)
	if meErr := <-meCh; meErr != nil {
		return nil, nil, false, meErr // 401 -> errSessionExpired
	}
	if err != nil {
		return nil, nil, false, err
	}
	// the listing names several machines the door could answer: no copy
	// may stream to or from whichever row sorted first — the anomaly is
	// refused in the copy arms' one wording, which admits it and points
	// at the listing. Several rows with nothing answerable are not this
	// refusal: dead rows are no machines, so below they read as none
	// and the ask places, exactly as an empty listing does.
	if which == doorRowSeveral {
		return nil, nil, false, errSeveralMachinesNoCopy
	}
	if err := ensurePubkey(a, me, stderr); err != nil {
		return nil, nil, false, err
	}
	if which == doorRowMachine && row.Live {
		return creds, row, false, nil
	}
	// no row was named, or the one named is not a machine running yet:
	// the door's ask decides wake or place from its own snapshot and
	// tells the story itself
	vm, _, err = askMachine(a, stderr, stderr)
	if err != nil {
		return nil, nil, false, err
	}
	return creds, vm, true, nil
}
