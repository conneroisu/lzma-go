package lzma_test

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/conneroisu/lzma-go"
)

const testDir = "lzma_test_files"

type generatedTest struct {
	name        string
	rawFilePath string
	sizeKnown   bool
	lzmaPath    string
}

func TestGeneratedFiles(t *testing.T) {
	// Skip if the test files directory doesn't exist
	if _, err := os.Stat(testDir); os.IsNotExist(err) {
		t.Skip("Test files directory not found. Run generate_testfiles.sh first")
	}

	// Find all raw test files
	rawFiles, err := filepath.Glob(filepath.Join(testDir, "raw", "*.dat"))
	if err != nil {
		t.Fatalf("Failed to list raw test files: %v", err)
	}

	if len(rawFiles) == 0 {
		t.Skip("No test files found. Run generate_testfiles.sh first")
	}

	// Create test cases
	var tests []generatedTest
	for _, rawFile := range rawFiles {
		baseName := filepath.Base(rawFile)
		testName := strings.TrimSuffix(baseName, ".dat")

		// Known size LZMA file
		knownSizeLzmaPath := filepath.Join(testDir, "encoded_size_known", testName+".lzma")
		if _, err := os.Stat(knownSizeLzmaPath); err == nil {
			tests = append(tests, generatedTest{
				name:        testName + "_known_size",
				rawFilePath: rawFile,
				sizeKnown:   true,
				lzmaPath:    knownSizeLzmaPath,
			})
		}

		// Unknown size LZMA file
		unknownSizeLzmaPath := filepath.Join(testDir, "encoded", testName+".lzma")
		if _, err := os.Stat(unknownSizeLzmaPath); err == nil {
			tests = append(tests, generatedTest{
				name:        testName + "_unknown_size",
				rawFilePath: rawFile,
				sizeKnown:   false,
				lzmaPath:    unknownSizeLzmaPath,
			})
		}
	}

	t.Logf("Running %d LZMA test cases", len(tests))

	// Run decoder tests
	t.Run("Decoder", func(t *testing.T) {
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				t.Parallel()
				testDecoder(t, tt)
			})
		}
	})

	// Run encoder tests
	t.Run("Encoder", func(t *testing.T) {
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				t.Parallel()
				testEncoder(t, tt)
			})
		}
	})

	// Run round-trip tests (encode then decode)
	t.Run("RoundTrip", func(t *testing.T) {
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				t.Parallel()
				testRoundTrip(t, tt)
			})
		}
	})
}

func testDecoder(t *testing.T, tt generatedTest) {
	// Read the expected raw data
	rawData, err := os.ReadFile(tt.rawFilePath)
	if err != nil {
		t.Fatalf("Failed to read raw file %s: %v", tt.rawFilePath, err)
	}

	// Read the LZMA compressed data
	compressedFile, err := os.Open(tt.lzmaPath)
	if err != nil {
		t.Fatalf("Failed to read LZMA file %s: %v", tt.lzmaPath, err)
	}
	defer compressedFile.Close()

	// Create a new decoder
	r := lzma.NewReader(compressedFile)
	defer r.Close()

	// Decode the data
	decodedData, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("Failed to decode LZMA data: %v", err)
	}

	// Verify the decoded data matches the original
	if !bytes.Equal(decodedData, rawData) {
		t.Errorf("Decoded data does not match original data")
		if len(decodedData) != len(rawData) {
			t.Errorf("Size mismatch: decoded %d bytes, expected %d bytes",
				len(decodedData), len(rawData))
		} else {
			// Find the first difference
			for i := range decodedData {
				if decodedData[i] != rawData[i] {
					t.Errorf("First difference at position %d: got %02x, expected %02x",
						i, decodedData[i], rawData[i])

					break
				}
			}
		}
	}
}

// testEncoder tests LZMA encoder by encoding raw data and verifying it can be decoded correctly.
func testEncoder(t *testing.T, tt generatedTest) {
	// Read the raw data
	rawData, err := os.ReadFile(tt.rawFilePath)
	if err != nil {
		t.Fatalf("Failed to read raw file %s: %v", tt.rawFilePath, err)
	}

	// Create a buffer for the encoded data
	var encodedBuf bytes.Buffer

	// Create a new encoder
	var w io.WriteCloser
	if tt.sizeKnown {
		w, err = lzma.NewWriterSize(&encodedBuf, int64(len(rawData)))
	} else {
		w, err = lzma.NewWriter(&encodedBuf)
	}
	if err != nil {
		t.Fatalf("Failed to create LZMA writer: %v", err)
	}

	// Write the raw data to the encoder
	n, err := w.Write(rawData)
	if err != nil {
		t.Fatalf("Failed to encode data: %v", err)
	}
	if n != len(rawData) {
		t.Fatalf("Wrote %d bytes, expected to write %d bytes", n, len(rawData))
	}

	// Close the encoder to flush any remaining data
	err = w.Close()
	if err != nil {
		t.Fatalf("Failed to close encoder: %v", err)
	}

	// Create a decoder to verify the encoded data
	r := lzma.NewReader(bytes.NewReader(encodedBuf.Bytes()))
	defer r.Close()

	// Decode the data
	decodedData, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("Failed to decode the encoded data: %v", err)
	}

	// Verify the decoded data matches the original
	if !bytes.Equal(decodedData, rawData) {
		t.Errorf("Round-trip encoding/decoding failed")
		if len(decodedData) != len(rawData) {
			t.Errorf("Size mismatch: decoded %d bytes, expected %d bytes",
				len(decodedData), len(rawData))
		} else {
			// Find the first difference
			for i := range decodedData {
				if decodedData[i] != rawData[i] {
					t.Errorf("First difference at position %d: got %02x, expected %02x",
						i, decodedData[i], rawData[i])

					break
				}
			}
		}
	}
}

func testRoundTrip(t *testing.T, tt generatedTest) {
	// Read the raw data
	rawData, err := os.ReadFile(tt.rawFilePath)
	if err != nil {
		t.Fatalf("Failed to read raw file %s: %v", tt.rawFilePath, err)
	}

	// First compress the data
	var compressedBuf bytes.Buffer
	var w io.WriteCloser
	if tt.sizeKnown {
		w, err = lzma.NewWriterSize(&compressedBuf, int64(len(rawData)))
	} else {
		w, err = lzma.NewWriter(&compressedBuf)
	}
	if err != nil {
		t.Fatalf("Failed to create LZMA writer: %v", err)
	}

	// Write the data to compress
	_, err = w.Write(rawData)
	if err != nil {
		t.Fatalf("Failed to write data to encoder: %v", err)
	}
	err = w.Close()
	if err != nil {
		t.Fatalf("Failed to close encoder: %v", err)
	}

	// Now decompress the data
	r := lzma.NewReader(bytes.NewReader(compressedBuf.Bytes()))
	defer r.Close()

	// Read the decompressed data
	decompressedData, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("Failed to decompress data: %v", err)
	}

	// Verify the decompressed data matches the original
	if !bytes.Equal(decompressedData, rawData) {
		t.Errorf("Round-trip compression/decompression failed")
		if len(decompressedData) != len(rawData) {
			t.Errorf("Size mismatch: got %d bytes, expected %d bytes",
				len(decompressedData), len(rawData))
		} else {
			// Find the first difference
			for i := range decompressedData {
				if decompressedData[i] != rawData[i] {
					t.Errorf("First difference at position %d: got %02x, expected %02x",
						i, decompressedData[i], rawData[i])

					break
				}
			}
		}
	}
}

// TestGeneratedFilesParallel runs the same tests as TestGeneratedFiles but in parallel.
//
// This helps detect race conditions and other concurrency issues.
func TestGeneratedFilesParallel(t *testing.T) {
	// Skip if the test files directory doesn't exist
	if _, err := os.Stat(testDir); os.IsNotExist(err) {
		t.Skip("Test files directory not found. Run generate_testfiles.sh first")
	}

	// Find all raw test files
	rawFiles, err := filepath.Glob(filepath.Join(testDir, "raw", "*.dat"))
	if err != nil {
		t.Fatalf("Failed to list raw test files: %v", err)
	}

	if len(rawFiles) == 0 {
		t.Skip("No test files found. Run generate_testfiles.sh first")
	}

	// Create test cases - use only a subset for parallel testing to avoid excessive resource usage
	var tests []generatedTest
	maxParallelTests := min(runtime.NumCPU()*2, len(rawFiles))

	for i := range maxParallelTests {
		rawFile := rawFiles[i]
		baseName := filepath.Base(rawFile)
		testName := strings.TrimSuffix(baseName, ".dat")

		// Known size LZMA file
		knownSizeLzmaPath := filepath.Join(testDir, "encoded_size_known", testName+".lzma")
		if _, err := os.Stat(knownSizeLzmaPath); err == nil {
			tests = append(tests, generatedTest{
				name:        testName + "_known_size",
				rawFilePath: rawFile,
				sizeKnown:   true,
				lzmaPath:    knownSizeLzmaPath,
			})
		}
	}

	t.Logf("Running %d parallel LZMA tests", len(tests))

	// Run encoder tests in parallel
	t.Run("ParallelEncoder", func(t *testing.T) {
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				t.Parallel()
				testEncoder(t, tt)
			})
		}
	})

	// Run decoder tests in parallel
	t.Run("ParallelDecoder", func(t *testing.T) {
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				t.Parallel()
				testDecoder(t, tt)
			})
		}
	})

	// Run round-trip tests in parallel
	t.Run("ParallelRoundTrip", func(t *testing.T) {
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				t.Parallel()
				testRoundTrip(t, tt)
			})
		}
	})
}

// BenchmarkEncode benchmarks the encoder with various file sizes.
func BenchmarkEncode(b *testing.B) {
	// Skip if the test files directory doesn't exist
	testDir := "lzma_test_files"
	if _, err := os.Stat(testDir); os.IsNotExist(err) {
		b.Skip("Test files directory not found. Run generate_testfiles.sh first")
	}

	// Find all raw test files
	rawFiles, err := filepath.Glob(filepath.Join(testDir, "raw", "*.dat"))
	if err != nil {
		b.Fatalf("Failed to list raw test files: %v", err)
	}

	if len(rawFiles) == 0 {
		b.Skip("No test files found. Run generate_testfiles.sh first")
	}

	// Group files by size range for benchmarking
	var smallFiles, mediumFiles, largeFiles []string
	for _, file := range rawFiles {
		info, err := os.Stat(file)
		if err != nil {
			b.Fatalf("Failed to stat file %s: %v", file, err)
		}

		size := info.Size()
		switch {
		case size < 1000:
			smallFiles = append(smallFiles, file)
		case size < 10000:
			mediumFiles = append(mediumFiles, file)
		default:
			largeFiles = append(largeFiles, file)
		}
	}

	// Benchmark small files
	if len(smallFiles) > 0 {
		benchmarkFileGroup(b, "SmallFiles", smallFiles)
	}

	// Benchmark medium files
	if len(mediumFiles) > 0 {
		benchmarkFileGroup(b, "MediumFiles", mediumFiles)
	}

	// Benchmark large files
	if len(largeFiles) > 0 {
		benchmarkFileGroup(b, "LargeFiles", largeFiles)
	}
}

// benchmarkFileGroup benchmarks encoding for a group of files.
func benchmarkFileGroup(b *testing.B, name string, files []string) {
	b.Run(name, func(b *testing.B) {
		// Use the first file from the group for benchmarking
		file := files[0]
		info, err := os.Stat(file)
		if err != nil {
			b.Fatalf("Failed to stat file %s: %v", file, err)
		}

		// Read the raw data
		rawData, err := os.ReadFile(file)
		if err != nil {
			b.Fatalf("Failed to read raw file %s: %v", file, err)
		}

		b.SetBytes(info.Size())
		b.ResetTimer()

		for b.Loop() {
			var buf bytes.Buffer
			w, err := lzma.NewWriter(&buf)
			if err != nil {
				b.Fatalf("Failed to create LZMA writer: %v", err)
			}

			if _, err := w.Write(rawData); err != nil {
				b.Fatalf("Failed to encode data: %v", err)
			}

			if err := w.Close(); err != nil {
				b.Fatalf("Failed to close encoder: %v", err)
			}
		}
	})
}

// BenchmarkDecode benchmarks the decoder with various file sizes.
func BenchmarkDecode(b *testing.B) {
	// Skip if the test files directory doesn't exist
	testDir := "lzma_test_files"
	if _, err := os.Stat(testDir); os.IsNotExist(err) {
		b.Skip("Test files directory not found. Run generate_testfiles.sh first")
	}

	// Find all lzma test files
	lzmaFiles, err := filepath.Glob(filepath.Join(testDir, "encoded", "*.lzma"))
	if err != nil {
		b.Fatalf("Failed to list LZMA test files: %v", err)
	}

	if len(lzmaFiles) == 0 {
		b.Skip("No LZMA files found. Run generate_testfiles.sh first")
	}

	// Group files by size range for benchmarking
	var smallFiles, mediumFiles, largeFiles []string
	for _, file := range lzmaFiles {
		// Get the size of the raw file
		baseName := filepath.Base(file)
		testName := strings.TrimSuffix(baseName, ".lzma")
		rawFile := filepath.Join(testDir, "raw", testName+".dat")

		info, err := os.Stat(rawFile)
		if err != nil {
			continue // Skip if we can't find the raw file
		}

		size := info.Size()
		switch {
		case size < 1000:
			smallFiles = append(smallFiles, file)
		case size < 10000:
			mediumFiles = append(mediumFiles, file)
		default:
			largeFiles = append(largeFiles, file)
		}
	}

	// Benchmark small files
	if len(smallFiles) > 0 {
		benchmarkDecodeGroup(b, "SmallFiles", smallFiles, testDir)
	}

	// Benchmark medium files
	if len(mediumFiles) > 0 {
		benchmarkDecodeGroup(b, "MediumFiles", mediumFiles, testDir)
	}

	// Benchmark large files
	if len(largeFiles) > 0 {
		benchmarkDecodeGroup(b, "LargeFiles", largeFiles, testDir)
	}
}

func benchmarkDecodeGroup(b *testing.B, name string, files []string, testDir string) {
	b.Run(name, func(b *testing.B) {
		// Use the first file from the group for benchmarking
		file := files[0]

		// Get the raw file to determine size
		baseName := filepath.Base(file)
		testName := strings.TrimSuffix(baseName, ".lzma")
		rawFile := filepath.Join(testDir, "raw", testName+".dat")

		info, err := os.Stat(rawFile)
		if err != nil {
			b.Fatalf("Failed to stat raw file %s: %v", rawFile, err)
		}

		// Read the LZMA data
		lzmaData, err := os.ReadFile(file)
		if err != nil {
			b.Fatalf("Failed to read LZMA file %s: %v", file, err)
		}

		b.SetBytes(info.Size())
		b.ResetTimer()

		for b.Loop() {
			r := lzma.NewReader(bytes.NewReader(lzmaData))

			// Read all data
			if _, err := io.Copy(io.Discard, r); err != nil {
				b.Fatalf("Failed to decode data: %v", err)
			}

			r.Close()
		}
	})
}

func TestMain(m *testing.M) {
	// Run the tests
	exitCode := m.Run()

	// Exit with the same code that the tests returned
	os.Exit(exitCode)
}
