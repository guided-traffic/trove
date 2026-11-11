package postgres

import (
	"strings"
	"testing"
	"testing/fstest"
)

func files(m map[string]string) fstest.MapFS {
	fs := fstest.MapFS{}
	for k, v := range m {
		fs[k] = &fstest.MapFile{Data: []byte(v)}
	}
	return fs
}

func TestEmbeddedSteps(t *testing.T) {
	t.Run("R-ARCH-026 the embedded migrations parse with valid headers", func(t *testing.T) {
		steps, err := EmbeddedSteps()
		if err != nil {
			t.Fatal(err)
		}
		if len(steps) < 4 || steps[0].Name != "00001_foundation" {
			t.Fatalf("steps = %+v", steps)
		}
		// A contract step is rare and named: it ships one release after the release that stopped using the
		// object, or only loosens (Q-BAS-48), and a new one is added to this list on purpose.
		// 00056_key_grammar replaces the key checks of repositories and projects by the OCI component grammar (Q-OCI-3): a DROP, and a
		// tightening that a release before the one it names would meet as a failing insert.
		// 00058_upload_session_indexes replaces the cleanup index of 00054 by one that orders inside a state (Q-STOR-176).
		// 00057_upload_session_tails drops the two tail columns of 00054, which is in no release, for a table of segments (Q-STOR-174).
		// 00075_principal_builtins deletes the grants that name the principals anonymous and authenticated, the principal authenticated and
		// its kind (Q-AUTHZ-279): a release before it would meet a principal it expects (R-AUTH-016).
		contract := map[string]bool{"00033_bastion_kinds": true, "00056_key_grammar": true, "00057_upload_session_tails": true, "00058_upload_session_indexes": true, "00075_principal_builtins": true}
		for _, s := range steps {
			if s.Phase != phaseExpand && !contract[s.Name] {
				t.Errorf("%s: an unlisted contract step: the migrations are expand steps unless a decision says otherwise, got %s", s.Name, s.Phase)
			}
			if s.Phase == phaseExpand && contract[s.Name] {
				t.Errorf("%s is listed as a contract step but is %s", s.Name, s.Phase)
			}
		}
	})
	t.Run("R-HA-018 requiredSchema is the newest expand step", func(t *testing.T) {
		steps, _ := EmbeddedSteps()
		if got := RequiredSchema(); got != steps[len(steps)-1].Version {
			t.Fatalf("RequiredSchema() = %d", got)
		}
	})
}

// TestParseSteps holds the expand/contract rules of R-HA-018 (strict), which are switched off until the first stable
// release (EnforceMigrationPhases, Q-ARCH-340): it runs them on, so they are ready when the switch is set again.
func TestParseSteps(t *testing.T) {
	ok := "-- trove:phase expand\nCREATE TABLE a (id int);\n"
	cases := []struct {
		name  string
		files map[string]string
		want  string // substring of the error; empty means success
	}{
		{"R-ARCH-026 a valid expand step", map[string]string{"00001_a.sql": ok}, ""},
		{
			"R-ARCH-026 a file without a phase header is refused",
			map[string]string{"00001_a.sql": "CREATE TABLE a (id int);\n"},
			"missing -- trove:phase",
		},
		{
			"R-ARCH-026 an unknown phase is refused",
			map[string]string{"00001_a.sql": "-- trove:phase sideways\nSELECT 1;\n"},
			"expand or contract",
		},
		{
			"R-ARCH-026 two phase headers are refused",
			map[string]string{"00001_a.sql": "-- trove:phase expand\n-- trove:phase contract\nSELECT 1;\n"},
			"more than one",
		},
		{
			"R-ARCH-026 a contract step without requires-app is refused",
			map[string]string{"00001_a.sql": "-- trove:phase contract\nDROP TABLE a;\n"},
			"requires-app",
		},
		{
			"R-ARCH-026 requires-app on an expand step is refused",
			map[string]string{"00001_a.sql": "-- trove:phase expand\n-- trove:requires-app >= 0.2.0\nSELECT 1;\n"},
			"contract steps only",
		},
		{
			"R-ARCH-026 a valid contract step",
			map[string]string{"00001_a.sql": "-- trove:phase contract\n-- trove:requires-app >= 0.2.0\nDROP TABLE a;\n"},
			"",
		},
		{
			"R-ARCH-026 requires-app must name a release",
			map[string]string{"00001_a.sql": "-- trove:phase contract\n-- trove:requires-app >= soon\nDROP TABLE a;\n"},
			"not a release version",
		},
		{
			"R-HA-018 DROP in an expand step is refused",
			map[string]string{"00001_a.sql": "-- trove:phase expand\nDROP TABLE a;\n"},
			"contract-class",
		},
		{
			"R-HA-018 RENAME in an expand step is refused",
			map[string]string{"00001_a.sql": "-- trove:phase expand\nALTER TABLE a RENAME TO b;\n"},
			"contract-class",
		},
		{
			"R-HA-018 SET NOT NULL in an expand step is refused",
			map[string]string{"00001_a.sql": "-- trove:phase expand\nALTER TABLE a ALTER COLUMN x SET NOT NULL;\n"},
			"contract-class",
		},
		{
			"R-HA-018 ON DELETE SET NULL and words in comments or strings are not contract statements",
			map[string]string{"00001_a.sql": "-- trove:phase expand\n-- we never DROP TABLE here\nCREATE TABLE a (x int REFERENCES b ON DELETE SET NULL, note text DEFAULT 'drop table');\n"},
			"",
		},
		{
			"R-HA-018 a dollar-quoted body is not scanned for contract statements",
			map[string]string{"00001_a.sql": "-- trove:phase expand\nCREATE FUNCTION f() RETURNS void LANGUAGE plpgsql AS $$ BEGIN DROP TABLE x; END $$;\n"},
			"",
		},
		{
			"R-HA-018 a no-transaction step holds exactly one statement",
			map[string]string{"00001_a.sql": "-- trove:phase expand\n-- trove:no-transaction\nCREATE INDEX CONCURRENTLY i ON a (x);\nCREATE INDEX CONCURRENTLY j ON a (y);\n"},
			"exactly one statement",
		},
		{
			"R-HA-018 a no-transaction step with one statement",
			map[string]string{"00001_a.sql": "-- trove:phase expand\n-- trove:no-transaction\nCREATE INDEX CONCURRENTLY i ON a (x);\n"},
			"",
		},
		{
			"R-ARCH-026 a gap in the numbers is refused",
			map[string]string{"00001_a.sql": ok, "00003_c.sql": ok},
			"without a gap",
		},
		{
			"R-ARCH-026 a badly named file is refused",
			map[string]string{"1_a.sql": ok},
			"NNNNN_<name>.sql",
		},
		{
			"R-ARCH-026 an unknown trove header is refused",
			map[string]string{"00001_a.sql": "-- trove:phase expand\n-- trove:frobnicate\nSELECT 1;\n"},
			"unknown header",
		},
		{
			"R-HA-022 a contract step names the background migrations it depends on, one header each",
			map[string]string{"00001_a.sql": "-- trove:phase contract\n-- trove:requires-app >= 0.2.0\n-- trove:requires-background backfill_a\n-- trove:requires-background backfill_b\nDROP TABLE a;\n"},
			"",
		},
		{
			"R-HA-022 requires-background on an expand step is refused",
			map[string]string{"00001_a.sql": "-- trove:phase expand\n-- trove:requires-background backfill_a\nSELECT 1;\n"},
			"requires-background belongs to contract steps only",
		},
		{
			"R-HA-022 requires-background names one migration by its name",
			map[string]string{"00001_a.sql": "-- trove:phase contract\n-- trove:requires-app >= 0.2.0\n-- trove:requires-background backfill_a, backfill_b\nDROP TABLE a;\n"},
			"must name one background migration",
		},
		{
			"R-HA-022 requires-background without a name is refused",
			map[string]string{"00001_a.sql": "-- trove:phase contract\n-- trove:requires-app >= 0.2.0\n-- trove:requires-background\nDROP TABLE a;\n"},
			"must name one background migration",
		},
		{
			"R-HA-022 a background migration named twice is refused",
			map[string]string{"00001_a.sql": "-- trove:phase contract\n-- trove:requires-app >= 0.2.0\n-- trove:requires-background backfill_a\n-- trove:requires-background backfill_a\nDROP TABLE a;\n"},
			"named twice",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := parseSteps(files(c.files), true)
			switch {
			case c.want == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)):
				t.Fatalf("error = %v, want it to contain %q", err, c.want)
			}
		})
	}
	t.Run("R-HA-022 the parsed step carries its background dependencies in header order", func(t *testing.T) {
		steps, err := ParseSteps(files(map[string]string{"00001_a.sql": "-- trove:phase contract\n-- trove:requires-app >= 0.2.0\n-- trove:requires-background backfill_b\n-- trove:requires-background backfill_a\nDROP TABLE a;\n"}))
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Join(steps[0].RequiresBackground, ","); got != "backfill_b,backfill_a" {
			t.Fatalf("RequiresBackground = %q", got)
		}
	})
	t.Run("R-ARCH-026 the checksum covers the file bytes", func(t *testing.T) {
		a, _ := parseSteps(files(map[string]string{"00001_a.sql": ok}), true)
		b, _ := parseSteps(files(map[string]string{"00001_a.sql": ok + "-- edited\n"}), true)
		if a[0].Checksum == b[0].Checksum {
			t.Fatal("an edited file keeps its checksum")
		}
	})
}

// TestParseStepsRelaxed holds the rules while the expand/contract rules are switched off (Q-ARCH-340): every header is
// optional, a file without -- trove:phase is an expand step, and the phase does not restrict the SQL. The rules that do
// not rest on expand/contract stay.
func TestParseStepsRelaxed(t *testing.T) {
	cases := []struct {
		name  string
		files map[string]string
		want  string // substring of the error; empty means success
	}{
		{"R-ARCH-026 a file without a header is a valid step", map[string]string{"00001_a.sql": "CREATE TABLE a (id int);\n"}, ""},
		{
			"R-HA-018 DROP, RENAME and SET NOT NULL are allowed in a step without a phase header",
			map[string]string{"00001_a.sql": "DROP TABLE a;\nALTER TABLE b RENAME TO c;\nALTER TABLE c ALTER COLUMN x SET NOT NULL;\n"},
			"",
		},
		{
			"R-HA-018 DROP, RENAME and SET NOT NULL are allowed in an expand step",
			map[string]string{"00001_a.sql": "-- trove:phase expand\nDROP TABLE a;\nALTER TABLE b RENAME TO c;\nALTER TABLE c ALTER COLUMN x SET NOT NULL;\n"},
			"",
		},
		{
			"R-ARCH-026 requires-app without a phase header is refused",
			map[string]string{"00001_a.sql": "-- trove:requires-app >= 0.2.0\nSELECT 1;\n"},
			"contract steps only",
		},
		{
			"R-ARCH-026 a contract step without requires-app is refused",
			map[string]string{"00001_a.sql": "-- trove:phase contract\nDROP TABLE a;\n"},
			"requires-app",
		},
		{
			"R-ARCH-026 an unknown phase is refused",
			map[string]string{"00001_a.sql": "-- trove:phase sideways\nSELECT 1;\n"},
			"expand or contract",
		},
		{
			"R-ARCH-026 two phase headers are refused",
			map[string]string{"00001_a.sql": "-- trove:phase expand\n-- trove:phase contract\nSELECT 1;\n"},
			"more than one",
		},
		{
			"R-ARCH-026 an unknown trove header is refused",
			map[string]string{"00001_a.sql": "-- trove:frobnicate\nSELECT 1;\n"},
			"unknown header",
		},
		{
			"R-HA-018 a no-transaction step holds exactly one statement",
			map[string]string{"00001_a.sql": "-- trove:no-transaction\nCREATE INDEX CONCURRENTLY i ON a (x);\nCREATE INDEX CONCURRENTLY j ON a (y);\n"},
			"exactly one statement",
		},
		{
			"R-HA-018 semicolons in comments, strings and a dollar-quoted body do not count as statements",
			map[string]string{"00001_a.sql": "-- trove:no-transaction\n-- one; two\nCREATE FUNCTION f() RETURNS text LANGUAGE plpgsql AS $body$ BEGIN PERFORM 1; RETURN 'a;b'; END $body$;\n"},
			"",
		},
		{
			"R-ARCH-026 a file of comments only holds no statement",
			map[string]string{"00001_a.sql": "-- trove:no-transaction\n-- nothing here\n"},
			"no statement",
		},
		{
			"R-ARCH-026 a gap in the numbers is refused",
			map[string]string{"00001_a.sql": "SELECT 1;\n", "00003_c.sql": "SELECT 1;\n"},
			"without a gap",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := parseSteps(files(c.files), false)
			switch {
			case c.want == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)):
				t.Fatalf("error = %v, want it to contain %q", err, c.want)
			}
		})
	}
	t.Run("R-ARCH-026 a step without a phase header is an expand step without requires-app", func(t *testing.T) {
		steps, err := parseSteps(files(map[string]string{"00001_a.sql": "-- trove:no-transaction\nCREATE INDEX CONCURRENTLY i ON a (x);\n"}), false)
		if err != nil {
			t.Fatal(err)
		}
		if s := steps[0]; s.Phase != phaseExpand || s.RequiresApp != "" || !s.NoTx {
			t.Fatalf("step = phase %q, requires-app %q, no-transaction %v", s.Phase, s.RequiresApp, s.NoTx)
		}
	})
	t.Run("R-ARCH-026 ParseSteps applies the switch EnforceMigrationPhases", func(t *testing.T) {
		_, err := ParseSteps(files(map[string]string{"00001_a.sql": "CREATE TABLE a (id int);\n"}))
		if (err == nil) == EnforceMigrationPhases {
			t.Fatalf("a file without a phase header: error %v with EnforceMigrationPhases = %v", err, EnforceMigrationPhases)
		}
	})
}

// TestRequiredSchemaBothModes: under the expand/contract rules a binary needs its newest expand step, not the contract
// step it ships last; with the rules switched off (Q-ARCH-340) it needs the highest step it embeds.
func TestRequiredSchemaBothModes(t *testing.T) {
	steps, err := parseSteps(files(map[string]string{
		"00001_a.sql": "-- trove:phase expand\nCREATE TABLE a (id int);\n",
		"00002_b.sql": "-- trove:phase contract\n-- trove:requires-app >= 0.2.0\nDROP TABLE a;\n",
	}), true)
	if err != nil {
		t.Fatal(err)
	}
	t.Run("R-HA-018 with the rules on, requiredSchema is the newest expand step", func(t *testing.T) {
		if got := requiredSchema(steps, true); got != 1 {
			t.Fatalf("requiredSchema = %d, want 1", got)
		}
	})
	t.Run("R-HA-018 with the rules off, requiredSchema is the highest step, a contract step included", func(t *testing.T) {
		if got := requiredSchema(steps, false); got != 2 {
			t.Fatalf("requiredSchema = %d, want 2", got)
		}
	})
	t.Run("R-HA-018 RequiredSchema follows the switch", func(t *testing.T) {
		embedded, err := EmbeddedSteps()
		if err != nil {
			t.Fatal(err)
		}
		if got, want := RequiredSchema(), requiredSchema(embedded, EnforceMigrationPhases); got != want {
			t.Fatalf("RequiredSchema() = %d, want %d", got, want)
		}
	})
}
