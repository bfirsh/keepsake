package benchmark

import (
	"bytes"
	"context"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/semaphore"

	"replicate.ai/cli/pkg/hash"
	"replicate.ai/cli/pkg/param"
	"replicate.ai/cli/pkg/project"
	"replicate.ai/cli/pkg/storage"
)

// run a command and return stdout. If there is an error, print stdout/err and fail test
func replicate(b *testing.B, arg ...string) string {
	// Get absolute path to built binary
	_, currentFilename, _, _ := runtime.Caller(0)
	binPath, err := filepath.Abs(path.Join(path.Dir(currentFilename), "../release", runtime.GOOS, runtime.GOARCH, "replicate"))
	require.NoError(b, err)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd := exec.Command(binPath, arg...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		fmt.Println(stdout.String())
		fmt.Println(stderr.String())
		b.Fatal(err)
	}
	return stdout.String()

}

func replicateList(b *testing.B, workingDir string, numExperiments int) {
	out := replicate(b, "list", "-D", workingDir)

	// Check the output is sensible
	firstLine := strings.Split(out, "\n")[0]
	require.Contains(b, firstLine, "experiment")
	// numExperiments + heading + trailing \n
	require.Equal(b, numExperiments+2, len(strings.Split(out, "\n")))
	// TODO: check first line is reasonable
}

// Create lots of files in a working dir
func createLotsOfFiles(b *testing.B, dir string) {
	// Some 1KB files is a bit like a bit source directory
	content := []byte(strings.Repeat("a", 1000))
	for i := 1; i < 10; i++ {
		err := ioutil.WriteFile(path.Join(dir, fmt.Sprintf("%d", i)), content, 0644)
		require.NoError(b, err)
	}
}

// Create lots of experiments and commits
func createLotsOfExperiments(workingDir string, storage storage.Storage, numExperiments int) error {
	numCommits := 50

	maxWorkers := int64(25)

	group, ctx := errgroup.WithContext(context.Background())
	group.Go(func() error {
		sem := semaphore.NewWeighted(maxWorkers)

		for i := 0; i < numExperiments; i++ {
			if err := sem.Acquire(ctx, 1); err != nil {
				return err
			}
			group.Go(func() error {
				defer sem.Release(1)

				exp := project.NewExperiment(map[string]*param.Value{
					"learning_rate": param.Float(0.001),
				})
				if err := exp.Save(storage); err != nil {
					return fmt.Errorf("Error saving experiment: %w", err)
				}

				if err := project.CreateHeartbeat(storage, exp.ID, time.Now().Add(-24*time.Hour)); err != nil {
					return fmt.Errorf("Error creating heartbeat: %w", err)
				}

				for j := 0; j < numCommits; j++ {
					com := project.NewCommit(exp.ID, map[string]*param.Value{
						"accuracy": param.Float(0.987),
					})
					if err := com.Save(storage, workingDir); err != nil {
						return fmt.Errorf("Error saving commit: %w", err)
					}
				}
				return nil
			})
		}
		return nil
	})
	return group.Wait()
}

func BenchmarkReplicateDisk(b *testing.B) {
	// Create working dir
	workingDir, err := ioutil.TempDir("", "replicate-test")
	require.NoError(b, err)
	defer os.RemoveAll(workingDir)

	createLotsOfFiles(b, workingDir)

	// Create storage
	storageDir := path.Join(workingDir, ".replicate/storage")
	storage, err := storage.NewDiskStorage(storageDir)
	require.NoError(b, err)
	defer os.RemoveAll(storageDir)

	err = createLotsOfExperiments(workingDir, storage, 10)
	require.NoError(b, err)

	b.Run("list first run with 10 experiments", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			replicateList(b, workingDir, 10)
		}
	})

	err = createLotsOfExperiments(workingDir, storage, 10)
	require.NoError(b, err)

	b.Run("list first run with 20 experiments", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			replicateList(b, workingDir, 20)
		}
	})

	err = createLotsOfExperiments(workingDir, storage, 10)
	require.NoError(b, err)

	b.Run("list first run with 30 experiments", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			replicateList(b, workingDir, 30)
		}
	})
}

func BenchmarkReplicateS3(b *testing.B) {
	// Create working dir
	workingDir, err := ioutil.TempDir("", "replicate-test")
	require.NoError(b, err)
	defer os.RemoveAll(workingDir)

	createLotsOfFiles(b, workingDir)

	// Create a bucket
	bucketName := "replicate-test-benchmark-" + hash.Random()[0:10]
	err = storage.CreateS3Bucket("us-east-1", bucketName)
	require.NoError(b, err)
	defer func() {
		require.NoError(b, storage.DeleteS3Bucket("us-east-1", bucketName))
	}()
	// Even though CreateS3Bucket is supposed to wait until it exists, sometimes it doesn't
	time.Sleep(5 * time.Second)

	// replicate.yaml
	err = ioutil.WriteFile(
		path.Join(workingDir, "replicate.yaml"),
		[]byte(fmt.Sprintf("storage: s3://%s", bucketName)), 0644)
	require.NoError(b, err)

	// Create storage
	storage, err := storage.NewS3Storage(bucketName)
	require.NoError(b, err)

	err = createLotsOfExperiments(workingDir, storage, 10)
	require.NoError(b, err)

	b.Run("list first run with 10 experiments", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			replicateList(b, workingDir, 10)
		}
	})

	err = createLotsOfExperiments(workingDir, storage, 10)
	require.NoError(b, err)

	b.Run("list first run with 20 experiments", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			replicateList(b, workingDir, 20)
		}
	})

	err = createLotsOfExperiments(workingDir, storage, 10)
	require.NoError(b, err)

	b.Run("list first run with 30 experiments", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			replicateList(b, workingDir, 30)
		}
	})
}

func BenchmarkReplicateHelp(b *testing.B) {
	for i := 0; i < b.N; i++ {
		out := replicate(b, "--help")
		require.Contains(b, out, "Usage:")
	}
}