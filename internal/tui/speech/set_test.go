package speech

import "testing"

func TestSetIsFeedForATextThatOnlyGrew(t *testing.T) {
	t.Parallel()
	fed, set := NewVoice(30), NewVoice(30)
	text := "Two things overlapped.  The fix\tis small,\n\nand it holds.  "
	for end := range len(text) + 1 {
		set.Set(text[:end])
		speak(set, 0.05)
	}
	fed.Feed(text)
	speak(fed, 10)
	speak(set, 10)
	if string(fed.Text()) != string(set.Text()) {
		t.Errorf("Set left %q, Feed %q", string(set.Text()), string(fed.Text()))
	}
	if set.Head() != len(set.Text()) {
		t.Errorf("%d of %d runes are spoken", set.Head(), len(set.Text()))
	}
}

func TestSetKeepsWhatWasSpokenWhereTheTextStillAgrees(t *testing.T) {
	t.Parallel()
	v := NewVoice(30)
	v.Set("The answer is forty-one.")
	speak(v, 5)
	v.Set("The answer is forty-two.")
	if got := string(v.Text()[:v.Head()]); got != "The answer is forty-" {
		t.Fatalf("after the text changed %q counts as spoken", got)
	}
	if v.State() != Speaking {
		t.Errorf("the state is %v with words left to say", v.State())
	}
	speak(v, 5)
	if got := string(v.Text()[:v.Head()]); got != "The answer is forty-two." {
		t.Errorf("spoke %q", got)
	}
	// A text that got shorter loses what it no longer holds.
	v.Set("The answer")
	if v.Head() != len("The answer") {
		t.Errorf("%d runes are spoken of a text of ten", v.Head())
	}
	v.Set("")
	if v.Head() != 0 || len(v.Text()) != 0 {
		t.Errorf("an empty text has %d runes, %d spoken", len(v.Text()), v.Head())
	}
}

func TestSkipSaysTheRestAtOnceAndLeavesItDry(t *testing.T) {
	t.Parallel()
	v := NewVoice(30)
	v.Feed("One sentence. And another, a longer one, to be skipped.")
	speak(v, 0.5)
	if v.Head() == 0 || v.Head() == len(v.Text()) {
		t.Fatalf("%d runes are spoken after half a second", v.Head())
	}
	v.Skip()
	if v.Head() != len(v.Text()) {
		t.Fatalf("after Skip %d of %d runes are spoken", v.Head(), len(v.Text()))
	}
	// Nothing is fresh, the mouth is shut, and no beat is still to come.
	if fresh := v.Fresh(0.55); fresh != v.Head() {
		t.Errorf("runes from %d on are still fresh", fresh)
	}
	if v.Mouth() != Rest {
		t.Errorf("the mouth is %+v", v.Mouth())
	}
	if beats := speak(v, 1); beats != 0 {
		t.Errorf("beats %b after Skip", beats)
	}
	if v.State() != Waiting {
		t.Errorf("the state is %v with the stream open", v.State())
	}
	v.Close()
	if v.State() != Finished {
		t.Errorf("the state is %v after Close", v.State())
	}
	// More to say is said at the usual pace, and only that is fresh.
	v.Open()
	v.Feed(" And a third.")
	if v.State() != Speaking {
		t.Errorf("the state is %v with more to say", v.State())
	}
	speak(v, 0.2)
	if fresh := v.Fresh(0.55); fresh != len("One sentence. And another, a longer one, to be skipped.") {
		t.Errorf("runes from %d on are fresh", fresh)
	}
}

func TestResetStartsOverButKeepsCounting(t *testing.T) {
	t.Parallel()
	v := NewVoice(30)
	v.Feed("Hello there, everyone.")
	speak(v, 3)
	v.Close()
	count := v.Syllable().Count
	if count == 0 || v.State() != Finished {
		t.Fatalf("%d syllables, state %v", count, v.State())
	}
	v.Reset()
	if len(v.Text()) != 0 || v.Head() != 0 || v.State() != Waiting {
		t.Fatalf("after Reset: %d runes, %d spoken, state %v", len(v.Text()), v.Head(), v.State())
	}
	v.Feed("Again.")
	speak(v, 1)
	// A listener tells one syllable from the next by its number: the first
	// of the new text must not have the number of one that was heard.
	if got := v.Syllable().Count; got <= count {
		t.Errorf("the syllables are numbered %d after %d", got, count)
	}
}
