#!/bin/bash

# Define the subdirectories for each entry point
declare -A subdirs=(
    [utahrealestate]="./seekers/utahrealestate"
    [matcher]="./matcher"
    [scheduler]="./scheduler"
)

# Check if an entry point is provided
if [ -z "$1" ]; then
    echo "Usage: $0 <entry_point>"
    exit 1
fi

# Check if the provided entry point is valid
if ! [[ "${!subdirs[@]}" =~ "$1" ]]; then
    echo "Invalid entry point: $1"
    echo "Valid entry points: ${!subdirs[@]}"
    exit 1
fi

# Change to the subdirectory and run go run main.go
cd "${subdirs[$1]}"
go run main.go

# Return to the original directory
cd - > /dev/null