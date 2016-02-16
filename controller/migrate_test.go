package main

import (
	"github.com/flynn/flynn/pkg/cluster"
	"github.com/flynn/flynn/pkg/postgres"
	"github.com/flynn/flynn/pkg/random"

	. "github.com/flynn/flynn/Godeps/_workspace/src/github.com/flynn/go-check"
)

type MigrateSuite struct{}

var _ = Suite(&MigrateSuite{})

type testMigrator struct {
	c  *C
	db *postgres.DB
	id int
}

func (t *testMigrator) migrateTo(id int) {
	t.c.Assert((*migrations)[t.id:id].Migrate(t.db), IsNil)
	t.id = id
}

// TestMigrateJobStates checks that migrating to ID 9 does not break existing
// job records
func (MigrateSuite) TestMigrateJobStates(c *C) {
	db := setupTestDB(c, "controllertest_migrate_job_states")
	m := &testMigrator{c: c, db: db}

	// start from ID 7
	m.migrateTo(7)

	// insert a job
	hostID := "host1"
	uuid := random.UUID()
	jobID := cluster.GenerateJobID(hostID, uuid)
	appID := random.UUID()
	releaseID := random.UUID()
	c.Assert(db.Exec(`INSERT INTO apps (app_id, name) VALUES ($1, $2)`, appID, "migrate-app"), IsNil)
	c.Assert(db.Exec(`INSERT INTO releases (release_id) VALUES ($1)`, releaseID), IsNil)
	c.Assert(db.Exec(`INSERT INTO job_cache (job_id, app_id, release_id, state) VALUES ($1, $2, $3, $4)`, jobID, appID, releaseID, "up"), IsNil)

	// migrate to 8 and check job states are still constrained
	m.migrateTo(8)
	err := db.Exec(`UPDATE job_cache SET state = 'foo' WHERE job_id = $1`, jobID)
	c.Assert(err, NotNil)
	if !postgres.IsPostgresCode(err, postgres.ForeignKeyViolation) {
		c.Fatalf("expected postgres foreign key violation, got %s", err)
	}

	// migrate to 9 and check job IDs are correct, pending state is valid
	m.migrateTo(9)
	var clusterID, dbUUID, dbHostID string
	c.Assert(db.QueryRow("SELECT cluster_id, job_id, host_id FROM job_cache WHERE cluster_id = $1", jobID).Scan(&clusterID, &dbUUID, &dbHostID), IsNil)
	c.Assert(clusterID, Equals, jobID)
	c.Assert(dbUUID, Equals, uuid)
	c.Assert(dbHostID, Equals, hostID)
	c.Assert(db.Exec(`UPDATE job_cache SET state = 'pending' WHERE job_id = $1`, uuid), IsNil)
}

// TestMigrateReleaseArtifacts checks that migrating to ID 11 correctly
// migrates releases by creating appropriate records in the release_artifacts
// table
func (MigrateSuite) TestMigrateReleaseArtifacts(c *C) {
	db := setupTestDB(c, "controllertest_migrate_release_artifacts")
	m := &testMigrator{c: c, db: db}

	// start from ID 10
	m.migrateTo(10)

	// add some artifacts and releases
	releaseArtifacts := map[string]string{
		random.UUID(): random.UUID(),
		random.UUID(): random.UUID(),
		random.UUID(): random.UUID(),
	}
	for releaseID, artifactID := range releaseArtifacts {
		c.Assert(db.Exec(`INSERT INTO artifacts (artifact_id, type, uri) VALUES ($1, $2, $3)`, artifactID, "docker", "http://example.com/"+artifactID), IsNil)
		c.Assert(db.Exec(`INSERT INTO releases (release_id, artifact_id) VALUES ($1, $2)`, releaseID, artifactID), IsNil)
	}
	c.Assert(db.Exec(`INSERT INTO releases (release_id) VALUES ($1)`, random.UUID()), IsNil)

	// migrate to 11 and check release_artifacts was populated correctly
	m.migrateTo(11)
	rows, err := db.Query("SELECT release_id, artifact_id FROM release_artifacts")
	c.Assert(err, IsNil)
	defer rows.Close()
	actual := make(map[string]string)
	for rows.Next() {
		var releaseID, artifactID string
		c.Assert(rows.Scan(&releaseID, &artifactID), IsNil)
		actual[releaseID] = artifactID
	}
	c.Assert(rows.Err(), IsNil)
	c.Assert(actual, DeepEquals, releaseArtifacts)
}
