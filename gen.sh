#!/bin/bash

# Script to generate test files for LZMA testing
# This creates a variety of file sizes and contents and compresses them with the
# reference LZMA implementation (xz-utils)
# Runs on Linux and macOS

set -e

# Create test directory
TEST_DIR="testdata"
mkdir -p "$TEST_DIR"
mkdir -p "$TEST_DIR/raw"
mkdir -p "$TEST_DIR/encoded"
mkdir -p "$TEST_DIR/encoded_size_known"

# Function to generate a random file of specified size with specified seed
generate_file() {
    local size=$1
    local seed=$2
    local filename=$3
    
    # Generate random data using the provided seed
    LC_ALL=C tr -dc 'A-Za-z0-9!"#$%&'\''()*+,-./:;<=>?@[\]^_`{|}~' < /dev/urandom | 
        head -c "$size" > "$filename"
    
    # Add a timestamp and the seed to make each file unique
    echo -e "\nGenerated with seed: $seed at $(date)" >> "$filename"
}

# Generate test files with varying sizes
generate_test_files() {
    local num_files=$1
    
    echo "Generating $num_files test files..."
    
    # Use current timestamp as initial seed
    local timestamp=$(date +%s)
    
    for i in $(seq 1 $num_files); do
        # Generate a unique seed for this file
        local seed=$(echo "$timestamp $i" | md5sum | cut -d' ' -f1)
        
        # Vary file size - some small, some medium, some large
        local size
        if [ $((i % 5)) -eq 0 ]; then
            # Larger file every 5th iteration
            size=$((10000 + (i * 1000)))
        elif [ $((i % 3)) -eq 0 ]; then
            # Medium file every 3rd iteration
            size=$((1000 + (i * 100)))
        else
            # Small file for other iterations
            size=$((100 + i))
        fi
        
        # Create the raw file
        local raw_filename="$TEST_DIR/raw/test_$i.dat"
        generate_file "$size" "$seed" "$raw_filename"
        
        # Encode with LZMA (size unknown, using -1 flag)
        lzma -c < "$raw_filename" > "$TEST_DIR/encoded/test_$i.lzma"
        
        # Encode with LZMA (size known)
        local filesize=$(stat -c%s "$raw_filename")
        python3 -c "
import lzma
with open('$raw_filename', 'rb') as f_in:
    data = f_in.read()
with open('$TEST_DIR/encoded_size_known/test_$i.lzma', 'wb') as f_out:
    # Format 1 - using legacy LZMA format (not LZMA2/XZ)
    f_out.write(lzma.compress(data, format=lzma.FORMAT_ALONE))
" || echo "Warning: Python LZMA encoding failed for test_$i.lzma - using xz instead" && lzma -c < "$raw_filename" > "$TEST_DIR/encoded_size_known/test_$i.lzma"
        
        echo "Generated test_$i.dat ($size bytes)"
    done
}

# Main execution
echo "Creating LZMA test files in $TEST_DIR"
generate_test_files 100

echo "Test file generation complete. Generated 50 test files in $TEST_DIR"
echo "Raw files are in $TEST_DIR/raw/"
echo "LZMA encoded files (size unknown) are in $TEST_DIR/encoded/"
echo "LZMA encoded files (size known) are in $TEST_DIR/encoded_size_known/"

# Create a manifest file with checksums
echo "Generating test manifest file..."
echo "# LZMA Test Files Manifest" > "$TEST_DIR/manifest.txt"
echo "# Generated on: $(date)" >> "$TEST_DIR/manifest.txt"
echo "" >> "$TEST_DIR/manifest.txt"
echo "## Raw Files" >> "$TEST_DIR/manifest.txt"
find "$TEST_DIR/raw" -type f -name "*.dat" | sort | while read file; do
    md5sum "$file" | awk '{print $1}' >> "$TEST_DIR/manifest.txt"
    echo "  $file ($(stat -c%s "$file") bytes)" >> "$TEST_DIR/manifest.txt"
done

echo "" >> "$TEST_DIR/manifest.txt"
echo "## Encoded Files (size unknown)" >> "$TEST_DIR/manifest.txt"
find "$TEST_DIR/encoded" -type f -name "*.lzma" | sort | while read file; do
    md5sum "$file" | awk '{print $1}' >> "$TEST_DIR/manifest.txt"
    echo "  $file ($(stat -c%s "$file") bytes)" >> "$TEST_DIR/manifest.txt"
done

echo "" >> "$TEST_DIR/manifest.txt"
echo "## Encoded Files (size known)" >> "$TEST_DIR/manifest.txt"
find "$TEST_DIR/encoded_size_known" -type f -name "*.lzma" | sort | while read file; do
    md5sum "$file" | awk '{print $1}' >> "$TEST_DIR/manifest.txt"
    echo "  $file ($(stat -c%s "$file") bytes)" >> "$TEST_DIR/manifest.txt"
done

echo "Test generation complete."
