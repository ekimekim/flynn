package backup

import (
	"fmt"
	"io"
	"time"

	"github.com/flynn/flynn/controller/client"
	ct "github.com/flynn/flynn/controller/types"
)

func Run(client *controller.Client, out io.Writer) error {
	tw := NewTarWriter("flynn-backup-"+time.Now().UTC().Format("2006-01-02_150405"), out)
	defer tw.Close()

	// get app and release details for key apps
	data, err := getApps(client)
	if err != nil {
		return err
	}
	if err := tw.WriteJSON("flynn.json", data); err != nil {
		return err
	}

	fmt.Println("getting postgres release")
	pgRelease := data["postgres"].Release
	pgJob := &ct.NewJob{
		ReleaseID:  pgRelease.ID,
		Entrypoint: []string{"sh"},
		Cmd:        []string{"-c", "pg_dumpall --clean --if-exists | gzip -9"},
		Env: map[string]string{
			"PGHOST":     pgRelease.Env["PGHOST"],
			"PGUSER":     pgRelease.Env["PGUSER"],
			"PGPASSWORD": pgRelease.Env["PGPASSWORD"],
		},
		DisableLog: true,
	}
	if err := tw.WriteCommandOutput(client, "postgres.sql.gz", "postgres", pgJob); err != nil {
		return fmt.Errorf("error dumping database: %s", err)
	}

	fmt.Println("getting mysql release")
	mysqlRelease := data["mariadb"].Release
	mysqlJob := &ct.NewJob{
		ReleaseID:  mysqlRelease.ID,
		Entrypoint: []string{"sh"},
		Cmd:        []string{"-c", fmt.Sprintf("/usr/bin/mysqldump -h %s -u %s --all-databases | gzip -9", mysqlRelease.Env["MYSQL_HOST"], mysqlRelease.Env["MYSQL_USER"])},
		Env: map[string]string{
			"MYSQL_PWD": mysqlRelease.Env["MYSQL_PWD"],
		},
		DisableLog: true,
	}
	if err := tw.WriteCommandOutput(client, "mysql.sql.gz", "mariadb", mysqlJob); err != nil {
		return fmt.Errorf("error dumping database: %s", err)
	}
	return nil
}

func getApps(client *controller.Client) (map[string]*ct.ExpandedFormation, error) {
	appNames := []string{"postgres", "mariadb", "discoverd", "flannel", "controller"}
	data := make(map[string]*ct.ExpandedFormation, len(appNames))
	for _, name := range appNames {
		app, err := client.GetApp(name)
		if err != nil {
			return nil, fmt.Errorf("error getting %s app details: %s", name, err)
		}
		release, err := client.GetAppRelease(app.ID)
		if err != nil {
			return nil, fmt.Errorf("error getting %s app release: %s", name, err)
		}
		formation, err := client.GetFormation(app.ID, release.ID)
		if err != nil {
			return nil, fmt.Errorf("error getting %s app formation: %s", name, err)
		}
		artifact, err := client.GetArtifact(release.ArtifactID)
		if err != nil {
			return nil, fmt.Errorf("error getting %s app artifact: %s", name, err)
		}
		data[name] = &ct.ExpandedFormation{
			App:       app,
			Release:   release,
			Artifact:  artifact,
			Processes: formation.Processes,
		}
	}
	return data, nil
}
