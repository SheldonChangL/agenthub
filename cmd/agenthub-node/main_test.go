package main

import (
	"agenthub.local/agenthub/internal/api"
	"agenthub.local/agenthub/internal/model"
	"bytes"
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"agenthub.local/agenthub/internal/discovery"
	"agenthub.local/agenthub/internal/nodeconfig"
	"agenthub.local/agenthub/internal/wake"
)

// A full candidate list is a condition an attacker can hold this node in, by
// continuing to send. One log line per packet would turn a bounded list — which
// is what the candidate layer exists to keep bounded — into unbounded log
// output, which is the same flood arriving by another route.
func TestARecurringCandidateConditionIsLoggedOnceNotPerPacket(t *testing.T) {
	var logged bytes.Buffer
	log.SetOutput(&logged)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(os.Stderr)
		log.SetFlags(log.LstdFlags)
	})

	// A candidate list that is already full, so every packet hits the condition.
	candidates := discovery.NewCandidates("node_local0000000000", func(context.Context, string) (bool, error) {
		return false, nil
	}, func(string) error { return nil })
	source := netip.MustParseAddr("192.168.9.9")
	handle := candidateHandler(candidates)
	fill := make([]discovery.Announcement, 0, discovery.MaxCandidates+1)
	for i := 0; i <= discovery.MaxCandidates; i++ {
		fill = append(fill, discovery.Announcement{
			NodeID:  fmt.Sprintf("node_filler%09d", i),
			Address: source.String() + ":7463",
			// A fingerprint is what makes an announcement an offer to pair.
			Fingerprint: "1223 03EA 5E96 543A 2DD8 BFEA",
		})
	}
	for packet := 0; packet < 50; packet++ {
		handle(context.Background(), source, fill)
	}

	full := strings.Count(logged.String(), "candidate list is full")
	if full == 0 {
		t.Fatalf("a full list was never reported: %s", logged.String())
	}
	if full > 1 {
		t.Errorf("a full list was logged %d times for 50 packets; one condition, one line", full)
	}

	// And the throttle is per condition, not one gate over everything: a
	// different failure arriving while a full list is being reported has to be
	// reported too, or whoever is holding the list full silences the log.
	handle(context.Background(), netip.Addr{}, fill)
	if !strings.Contains(logged.String(), "could not read pairing offers") {
		t.Errorf("a second, different failure was swallowed by the first: %s", logged.String())
	}
}

// A flag passed with its default value is still a flag that was passed.
//
// -display-name "" means "hand the name back to the machine", and reading the
// flag's value cannot say whether an empty string was typed or the flag was
// left out. Getting this wrong makes a pinned name permanent, which is not a
// failure anything else here would catch: the node starts, keeps the old name,
// and looks like the rename simply did not take.
func TestWasSetDistinguishesAPassedFlagFromAnAbsentOne(t *testing.T) {
	for name, args := range map[string][]string{
		"passed empty":       {"-display-name", ""},
		"passed a value":     {"-display-name", "the machine on my desk"},
		"passed with equals": {"-display-name="},
	} {
		flags := flag.NewFlagSet("test", flag.ContinueOnError)
		flags.String("display-name", "", "")
		if err := flags.Parse(args); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !wasSet(flags, "display-name") {
			t.Errorf("%s (%q) reads as absent, so an empty value could never release a pinned name",
				name, args)
		}
	}

	flags := flag.NewFlagSet("test", flag.ContinueOnError)
	flags.String("display-name", "", "")
	flags.String("other", "", "")
	if err := flags.Parse([]string{"-other", "x"}); err != nil {
		t.Fatal(err)
	}
	if wasSet(flags, "display-name") {
		t.Error("an absent flag reads as passed, so every start would release the name")
	}
}

// The deadline the owner listener sets is the one the API is told about.
//
// They were two literals in two places, and changing either alone passed every
// test — while putting the wake stream back to holding a poll past the
// deadline that cuts it, which is the bug that made the whole feature not work
// in steady state. The API caps a poll below whatever it is told; if it is
// told the wrong number, the cap is against the wrong deadline.
func TestTheOwnerListenerAndTheAPIAgreeOnTheWriteDeadline(t *testing.T) {
	server := ownerServer("127.0.0.1:0", http.NotFoundHandler())
	if server.WriteTimeout != ownerWriteTimeout {
		t.Errorf("the listener writes for %s and the API is told %s",
			server.WriteTimeout, ownerWriteTimeout)
	}
	// And a poll capped against it leaves room to answer.
	if api.NewServer(nil, nil, nil, model.NodeIdentity{},
		api.WithWriteTimeout(ownerWriteTimeout)).WakeStreamWait(0) >= server.WriteTimeout {
		t.Error("a poll may run to the connection deadline, so it can never answer")
	}
}

// The ack window outlasts the deadline on the write it is waiting for.
//
// A taker acknowledges after writing the response, and that write can block on
// a reader that has stopped reading until this listener gives up on it. If the
// driver stops waiting first, a message still on its way is settled as one the
// agent never got — and the row it writes is the one the wake limits are
// counted from.
//
// Two literals in two packages before this: 90s in internal/wake and 60s here,
// with nothing between them. Putting the driver's window back to the handoff's
// five seconds passed every test in both packages. This is the same pair of
// drifting numbers as the write deadline above, one layer down.
func TestTheAckWindowOutlastsTheWriteItWaitsFor(t *testing.T) {
	if wake.AckWait <= ownerWriteTimeout {
		t.Errorf("the driver waits %s to be told a message went out, on a listener that "+
			"allows the write itself %s; a slow write is settled as a failed one. "+
			"Raising AckWait is not the only way out of this: it is bounded above too, "+
			"by the context one wake runs under — see "+
			"TestOneWakeFitsInsideTheContextItRunsUnder in internal/api",
			wake.AckWait, ownerWriteTimeout)
	}
}

// Shutdown ends held requests before waiting for handlers.
//
// http.Server.Shutdown waits for handlers to return and does not cancel their
// contexts, so a held wake stream kept the node alive to its own deadline: the
// shutdown reported a timeout and the peer listener was never closed. Nothing
// pinned the Drain call, and deleting it passed the whole suite while taking
// shutdown from 0.10s to 5.08s and exit 1.
func TestShutdownDrainsBeforeWaiting(t *testing.T) {
	var release sync.Once
	drained := make(chan struct{})
	// Released whatever happens, so a shutdown that never drains fails this
	// test instead of hanging it: an httptest server's Close waits for its
	// handlers, and a test that burns the whole CI timeout says less than one
	// that fails in three seconds.
	defer release.Do(func() { close(drained) })

	held := make(chan struct{})
	owner := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		close(held)
		<-drained
	}))
	peers := httptest.NewServer(http.NotFoundHandler())
	defer func() {
		release.Do(func() { close(drained) })
		owner.Close()
		peers.Close()
	}()

	go func() { _, _ = http.Get(owner.URL) }()
	<-held

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	started := time.Now()
	err := shutDown(ctx, drainer(func() { release.Do(func() { close(drained) }) }),
		owner.Config, peers.Config)
	if err != nil {
		t.Fatalf("shutDown() error = %v; a held request outlasted the budget, which is "+
			"what happens when nothing drains it first", err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Errorf("shutdown took %s with one held request", elapsed)
	}
}

// Both listeners are closed even when the first shutdown fails.
//
// A peer listener left open is a socket still accepting deliveries from the
// network after this process has decided to stop, and it was skipped by an
// early return whenever the owner listener timed out.
func TestShutdownClosesThePeerListenerEvenWhenTheOwnerOneFails(t *testing.T) {
	stuck := make(chan struct{})
	owner := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		<-stuck
	}))
	// Released before Close, not after: httptest.Server.Close waits for
	// handlers, and a deferred close runs last.
	defer func() { close(stuck); owner.Close() }()
	peers := httptest.NewServer(http.NotFoundHandler())
	peerURL := peers.URL
	defer peers.Close()

	reached := make(chan struct{})
	go func() { close(reached); _, _ = http.Get(owner.URL) }()
	<-reached
	time.Sleep(100 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	// A drainer that does nothing, so the owner shutdown really does time out.
	if err := shutDown(ctx, drainer(func() {}), owner.Config, peers.Config); err == nil {
		t.Fatal("a shutdown that could not finish reported success")
	}
	if _, err := http.Get(peerURL); err == nil {
		t.Error("the peer listener is still accepting after shutdown; an early return " +
			"on the owner listener's failure used to skip it entirely")
	}
}

type drainer func()

func (d drainer) Drain() { d() }

// run() reaches its listener and its shutdown through the extracted helpers.
//
// Both helpers are tested above and neither call site was. Putting the old
// inline listener back left the owner connection on a 30s write deadline while
// the API went on capping polls against 60s — the steady-state bug this branch
// exists to fix — and every test stayed green. Putting the old inline
// apiServer.Shutdown back took a SIGTERM from 0.15s to 5.08s with the peer
// listener never closed, also green.
//
// This reads run()'s source rather than calling it: run() parses flags, binds
// two real listeners and blocks on signals, so what can be pinned here is
// which helper it is wired to. That is text, not behaviour — it would be
// satisfied by the name in a comment — and it is worth having only because the
// behaviour on either side of the seam is covered and the seam was not.
func TestRunGoesThroughTheExtractedListenerAndShutdown(t *testing.T) {
	body := functionBody(t, "main.go", "func run() error {")

	for what, needle := range map[string]string{
		"the owner listener is built by ownerServer": "ownerServer(",
		"the shutdown goes through shutDown":         "shutDown(",
		"the API is told that same write deadline":   "api.WithWriteTimeout(ownerWriteTimeout)",
	} {
		if !strings.Contains(body, needle) {
			t.Errorf("run() has no %q, so %s is no longer true", needle, what)
		}
	}
	// The peer listener is still built inline, so only the owner one is named.
	for what, needle := range map[string]string{
		"the owner listener is inline again": "server := &http.Server{",
		"the shutdown is inline again":       ".Shutdown(",
	} {
		if strings.Contains(body, needle) {
			t.Errorf("run() contains %q: %s", needle, what)
		}
	}
}

// functionBody returns what lies between opener and the line that closes it at
// column zero, which gofmt guarantees exists.
func functionBody(t *testing.T, file, opener string) string {
	t.Helper()
	source, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	start := strings.Index(string(source), opener)
	if start < 0 {
		t.Fatalf("%s has no %q; this test pins a function that is gone", file, opener)
	}
	rest := string(source)[start+len(opener):]
	end := strings.Index(rest, "\n}\n")
	if end < 0 {
		t.Fatalf("no closing brace at column zero after %q in %s", opener, file)
	}
	return rest[:end]
}

// A flag given now decides this start and is recorded; a flag left off means
// whatever the node remembered. The distinction is "was it passed", not "does
// it differ from the default": -allow-lan=false is an owner turning something
// off, and reading that as absence would make a remembered switch impossible
// to close.
func TestFlagSettingsCollectsOnlyWhatTheCommandLineGave(t *testing.T) {
	parse := func(args ...string) nodeconfig.Partial {
		t.Helper()
		flags := flag.NewFlagSet("agenthub-node", flag.ContinueOnError)
		peerListen := flags.String("peer-listen", nodeconfig.DefaultPeerListen, "")
		allowLAN := flags.Bool("allow-lan", false, "")
		discover := flags.Bool("discover", false, "")
		autoWake := flags.Bool("auto-wake", false, "")
		flags.String("db", "", "")
		var declaredPrivate nodeconfig.StringList
		flags.Var(&declaredPrivate, "treat-as-private", "")
		if err := flags.Parse(args); err != nil {
			t.Fatal(err)
		}
		return flagSettings(flags, peerListen, allowLAN, discover, autoWake, declaredPrivate)
	}

	if given := parse("-db", "/tmp/x.db"); !given.Empty() {
		t.Fatalf("a command line with no remembered flag collected %+v", given)
	}

	off := parse("-allow-lan=false")
	if off.AllowLAN == nil || *off.AllowLAN {
		t.Fatalf("-allow-lan=false collected %v; a switch has to be closable", off.AllowLAN)
	}
	if off.Discover != nil || off.PeerListen != nil || off.TreatAsPrivate != nil {
		t.Fatalf("one flag collected others: %+v", off)
	}

	// Given at all, -treat-as-private replaces the whole declaration — empty
	// included, which is how the last range is withdrawn.
	both := parse("-treat-as-private", "10.9.0.0/16", "-treat-as-private", "122.122.0.0/16")
	if both.TreatAsPrivate == nil || len(*both.TreatAsPrivate) != 2 {
		t.Fatalf("treatAsPrivate = %v", both.TreatAsPrivate)
	}
	settings, sources := nodeconfig.Resolve(both, nodeconfig.Partial{
		TreatAsPrivate: func() *[]string { old := []string{"172.20.0.0/16"}; return &old }(),
	}, nodeconfig.DefaultSettings())
	if len(settings.TreatAsPrivate) != 2 || sources[nodeconfig.SettingTreatAsPrivate] != nodeconfig.SourceFlag {
		t.Fatalf("settings = %+v, sources = %v", settings, sources)
	}
}

// A remembered value can stop being valid with nothing changing on this
// command line — a renumbered network, a range that no longer covers the
// address. The refusal has to say where the value came from and how to replace
// it, or the owner reads an error about an address they never typed.
func TestARefusalOverARememberedValueSaysSo(t *testing.T) {
	settings := nodeconfig.Settings{PeerListen: "192.168.1.10:7463"}
	_, err := settings.Validate()
	if err == nil {
		t.Fatal("a LAN listener without -allow-lan was accepted")
	}
	explained := rememberedRefusal(err, map[string]string{
		nodeconfig.SettingPeerListen: nodeconfig.SourceRemembered,
	})
	for _, want := range []string{"remembering", "-peer-listen", "ah settings"} {
		if !strings.Contains(explained.Error(), want) {
			t.Errorf("the refusal lacks %q: %v", want, explained)
		}
	}
	// Nothing was remembered, so nothing is added: the flag the owner typed is
	// right there on their command line.
	plain := rememberedRefusal(err, map[string]string{nodeconfig.SettingPeerListen: nodeconfig.SourceFlag})
	if plain.Error() != err.Error() {
		t.Errorf("a refusal over a typed flag was decorated: %v", plain)
	}
}

// rememberingStore is a settingsStore that keeps what it was told, so a test
// can ask what actually reached the database rather than what was returned.
type rememberingStore struct {
	stored nodeconfig.Partial
	saves  int
	getErr error
}

func (s *rememberingStore) GetNodeSettings(context.Context) (nodeconfig.Partial, error) {
	return s.stored, s.getErr
}

func (s *rememberingStore) SaveNodeSettings(_ context.Context, settings nodeconfig.Partial) error {
	s.saves++
	s.stored = s.stored.Overlay(settings)
	return nil
}

func boolFlag(value bool) *bool       { return &value }
func stringFlag(value string) *string { return &value }

// A flag that cannot start this node must not be remembered, because a
// remembered one cannot be taken back: the next start refuses over a value
// that is on no command line, the service restarts forever, and the
// `ah settings set ...` the refusal names goes through an API a node that will
// not start is not serving.
func TestAnInvalidFlagIsNotRemembered(t *testing.T) {
	store := &rememberingStore{}
	given := nodeconfig.Partial{PeerListen: stringFlag("8.8.8.8:7463"), AllowLAN: boolFlag(true)}

	if _, err := applyStartupSettings(context.Background(), store, given, func(string, ...any) {}); err == nil {
		t.Fatal("a public peer listener was accepted")
	}
	if store.saves != 0 {
		t.Errorf("a refused configuration was written to the store %d time(s)", store.saves)
	}
	if !store.stored.Empty() {
		t.Fatalf("the store holds %+v after a refusal; the next start would refuse over it too", store.stored)
	}
	// The proof that this is about order and not about the error: the same
	// start with nothing given now succeeds, which is exactly what a service
	// restart does.
	after, err := applyStartupSettings(context.Background(), store, nodeconfig.Partial{}, func(string, ...any) {})
	if err != nil {
		t.Fatalf("the next start was still blocked: %v", err)
	}
	if after.settings.PeerListen != nodeconfig.DefaultPeerListen || after.settings.AllowLAN {
		t.Errorf("the next start ran with %+v, not the defaults", after.settings)
	}
}

// A valid flag is remembered, which is the whole reason these settings exist:
// `ah service install` needs only --db because the value given once is read
// back on every later start.
func TestAValidFlagIsRemembered(t *testing.T) {
	store := &rememberingStore{}
	given := nodeconfig.Partial{PeerListen: stringFlag("192.168.1.10:7463"), AllowLAN: boolFlag(true)}

	if _, err := applyStartupSettings(context.Background(), store, given, func(string, ...any) {}); err != nil {
		t.Fatalf("applyStartupSettings: %v", err)
	}
	stored, err := store.GetNodeSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stored.PeerListen == nil || *stored.PeerListen != "192.168.1.10:7463" {
		t.Errorf("peerListen read back as %v; nothing was remembered", stored.PeerListen)
	}
	if stored.AllowLAN == nil || !*stored.AllowLAN {
		t.Errorf("allowLan read back as %v; nothing was remembered", stored.AllowLAN)
	}

	// And it applies on a later start with no flags at all.
	next, err := applyStartupSettings(context.Background(), store, nodeconfig.Partial{}, func(string, ...any) {})
	if err != nil {
		t.Fatalf("the remembered configuration did not start: %v", err)
	}
	if next.settings.PeerListen != "192.168.1.10:7463" || !next.settings.AllowLAN {
		t.Errorf("the next start ran with %+v", next.settings)
	}
	if next.sources[nodeconfig.SettingPeerListen] != nodeconfig.SourceRemembered {
		t.Errorf("peerListen came from %q, not %q", next.sources[nodeconfig.SettingPeerListen], nodeconfig.SourceRemembered)
	}
}

// Every setting, every start. A remembered -allow-lan appears on no command
// line, so these lines are the only place an owner can see that this machine
// is serving the network — which the README promises they are.
func TestTheStartupLogSaysEverySettingAndWhereItCameFrom(t *testing.T) {
	store := &rememberingStore{stored: nodeconfig.Partial{
		PeerListen: stringFlag("192.168.1.10:7463"),
		AllowLAN:   boolFlag(true),
	}}
	var lines []string
	startup, err := applyStartupSettings(context.Background(), store,
		nodeconfig.Partial{Discover: boolFlag(true)},
		func(format string, args ...any) { lines = append(lines, fmt.Sprintf(format, args...)) })
	if err != nil {
		t.Fatalf("applyStartupSettings: %v", err)
	}
	logged := strings.Join(lines, "\n")
	for _, want := range nodeconfig.Describe(startup.settings, startup.sources) {
		if !strings.Contains(logged, "setting "+want) {
			t.Errorf("the start-up log never said %q:\n%s", want, logged)
		}
	}
	// Named, so that "remembered" and "typed just now" stay distinguishable in
	// the one place a remembered switch is visible at all.
	for _, want := range []string{
		"setting allow-lan = true (remembered)",
		"setting discover = true (flag)",
	} {
		if !strings.Contains(logged, want) {
			t.Errorf("the start-up log lacks %q:\n%s", want, logged)
		}
	}
}
