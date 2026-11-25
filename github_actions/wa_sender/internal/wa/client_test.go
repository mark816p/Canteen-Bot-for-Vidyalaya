package wa

import "testing"

func TestParseGroupJID(t *testing.T) {
	valid := "120363012345678901@g.us"
	jid, err := ParseGroupJID(valid)
	if err != nil {
		t.Fatalf("expected valid group JID, got err=%v", err)
	}
	if jid.String() != valid {
		t.Fatalf("got %q, want %q", jid.String(), valid)
	}

	invalidCases := []string{
		"",
		"919876543210@s.whatsapp.net", // must reject DMs
		"120363012345678901@newsletter",
		"not-a-jid",
	}
	for _, tc := range invalidCases {
		if _, err := ParseGroupJID(tc); err == nil {
			t.Errorf("expected error for non-group/invalid JID %q", tc)
		}
	}
}
