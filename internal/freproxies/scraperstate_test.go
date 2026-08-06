package freproxies

import (
	"context"
	"testing"
)

// Enabled state must be settable in both directions for every source,
// independent of what the source ships as its default.
//
// The store only kept a "disabled" set, and IsScraperEnabled fell back to
// DefaultEnabled() when a source was absent from it. So enabling a source that
// ships default-off did an SRem of something that was never there and left the
// source off. That silently breaks two things: the panel's enable button for
// default-off sources, and any automation that re-enables a source it measured
// as recovered.
func TestScraperEnabledStateIsExplicitInBothDirections(t *testing.T) {
	backends := map[string]func(t *testing.T) Store{
		"memory": func(t *testing.T) Store { return NewMemoryStore() },
		"redis": func(t *testing.T) Store {
			s, _ := newFakeRedisStore(t)
			return s
		},
	}
	for name, open := range backends {
		t.Run(name, func(t *testing.T) {
			store := open(t)
			ctx := context.Background()

			// A source that ships disabled, then is turned on.
			const shipsOff = "oversized-source"
			if err := store.SetScraperEnabled(ctx, shipsOff, true); err != nil {
				t.Fatal(err)
			}
			on, err := store.IsScraperEnabled(ctx, shipsOff, false)
			if err != nil {
				t.Fatal(err)
			}
			if !on {
				t.Error("enabling a source that ships default-off must take effect; " +
					"it stayed off, so the panel's enable button and automated re-enable are both no-ops")
			}

			// And back off again.
			if err := store.SetScraperEnabled(ctx, shipsOff, false); err != nil {
				t.Fatal(err)
			}
			if on, _ = store.IsScraperEnabled(ctx, shipsOff, false); on {
				t.Error("disabling it again must stick")
			}

			// A source that ships enabled, turned off then on.
			const shipsOn = "healthy-source"
			if err := store.SetScraperEnabled(ctx, shipsOn, false); err != nil {
				t.Fatal(err)
			}
			if on, _ = store.IsScraperEnabled(ctx, shipsOn, true); on {
				t.Error("disabling a default-on source must take effect")
			}
			if err := store.SetScraperEnabled(ctx, shipsOn, true); err != nil {
				t.Fatal(err)
			}
			if on, _ = store.IsScraperEnabled(ctx, shipsOn, true); !on {
				t.Error("re-enabling a default-on source must take effect")
			}

			// An untouched source still follows its shipped default, so existing
			// configuration keeps its meaning.
			if on, _ = store.IsScraperEnabled(ctx, "never-touched", true); !on {
				t.Error("an untouched default-on source should read enabled")
			}
			if on, _ = store.IsScraperEnabled(ctx, "never-touched-2", false); on {
				t.Error("an untouched default-off source should read disabled")
			}
		})
	}
}

// Setting the same state twice must be idempotent: automation re-runs, and a
// second apply must not flip anything back.
func TestScraperEnabledIsIdempotent(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if err := store.SetScraperEnabled(ctx, "src", false); err != nil {
			t.Fatal(err)
		}
	}
	if on, _ := store.IsScraperEnabled(ctx, "src", true); on {
		t.Error("three disables must leave the source disabled, not flipped")
	}
	for i := 0; i < 3; i++ {
		if err := store.SetScraperEnabled(ctx, "src", true); err != nil {
			t.Fatal(err)
		}
	}
	if on, _ := store.IsScraperEnabled(ctx, "src", true); !on {
		t.Error("three enables must leave the source enabled")
	}
}
