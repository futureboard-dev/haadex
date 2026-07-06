#!/bin/bash

# A script to concatenate the content of specified code files and copy it to the clipboard.
# Project agnostic: Supports Go, Python, JS/TS, Rust, etc.

# --- CONFIGURATION / EXTENSIONS ---

# Files to include
EXTENSIONS=(
    "*.go" "*.py" "*.ts" "*.tsx" "*.js" "*.jsx" "*.mjs" "*.py" 
    "*.rs" "*.c" "*.cpp" "*.h" "*.cs" "*.java" "*.php"
    "*.md" "*.sh" "*.sql" "*.yaml" "*.yml" "*.json" "*.toml" "*.proto"
    "*.css" "*.scss" "*.html"
)

# Directories to ignore
EXCLUSIONS=(
    "*/.git/*" "*/node_modules/*" "*/vendor/*" "*/target/*" 
    "*/dist/*" "*/build/*" "*/.next/*" "*/.vscode/*" 
    "*/__pycache__/*" "*/.venv/*" "*/venv/*" "*/env/*" 
    "*/.pytest_cache/*" "*/.terraform/*" "*/local/*" "*/uploads/*"
)

# Critical config files to always include first for LLM context
CONFIG_FILES=(
    "package.json" "tsconfig.json" "go.mod" "requirements.txt" 
    "pyproject.toml" "Cargo.toml" "Makefile" "Dockerfile" 
    "docker-compose.yml" "env_example" ".env.example" "README.md"
)

# --- HELPER FUNCTIONS ---

find_code_files() {
    local base_dir=$1
    local only_today=$2
    
    local find_cmd=(find "$base_dir" -type f)

    # Build the extension filter: ( -name "*.go" -o -name "*.py" ... )
    find_cmd+=( "(" )
    for i in "${!EXTENSIONS[@]}"; do
        if [ "$i" -gt 0 ]; then find_cmd+=( "-o" ); fi
        find_cmd+=( "-name" "${EXTENSIONS[$i]}" )
    done
    find_cmd+=( ")" )

    # Build the exclusion filter: ! -path "*/node_modules/*" ! -path ...
    for exc in "${EXCLUSIONS[@]}"; do
        find_cmd+=( "!" "-path" "$exc" )
    done

    # Apply date filter if requested
    if [ "$only_today" == "true" ]; then
        local date_today=$(date "+%Y-%m-%d")
        find_cmd+=(-newermt "$date_today")
    fi

    "${find_cmd[@]}" -print0
}

copy_to_clipboard() {
    local content=$1
    if command -v pbcopy &>/dev/null; then
        echo -e "$content" | pbcopy
    elif command -v xclip &>/dev/null; then
        echo -e "$content" | xclip -selection clipboard
    elif command -v clip.exe &>/dev/null; then
        echo -e "$content" | clip.exe
    else
        echo "Error: No clipboard utility found (pbcopy, xclip, or clip.exe)."
        exit 1
    fi
}

# --- MAIN SCRIPT LOGIC ---

OUTPUT=""
MODE=""
INPUT_PATH=""
ONLY_TODAY="false"

while [[ $# -gt 0 ]]; do
  case $1 in
    -f) MODE="filelist"; shift ;;
    --today) ONLY_TODAY="true"; shift ;;
    *) 
      if [[ -z "$MODE" ]]; then
        MODE="directory"
        INPUT_PATH=$1
      fi
      shift ;;
  esac
done

if [[ -z "$MODE" ]]; then
    echo "Usage: $0 <path> [--today]  OR  $0 -f [--today]"
    exit 1
fi

# 1. Prepend Critical Config Files (Context)
for CFILE in "${CONFIG_FILES[@]}"; do
    if [ -f "$CFILE" ]; then
        # Skip if scanning current dir in normal mode to avoid duplicates
        if [ "$MODE" == "directory" ] && [ "$ONLY_TODAY" == "false" ]; then
            if [[ "$INPUT_PATH" == "." ]] || [[ "$INPUT_PATH" == "./" ]]; then
                continue
            fi
        fi
        OUTPUT+="\n\n--- File: $CFILE ---\n"
        OUTPUT+="$(<"$CFILE" tr -d '\0')\n"
    fi
done

# 2. Process Files
if [ "$MODE" == "filelist" ]; then
    FILE_LIST="getcode_files.txt"
    [ ! -f "$FILE_LIST" ] && echo "Error: $FILE_LIST not found." && exit 1
    
    DATE_TODAY=$(date "+%Y-%m-%d")
    while IFS= read -r FILE || [ -n "$FILE" ]; do
        [[ -z "$FILE" || "$FILE" == \#* ]] && continue
        [ ! -f "$FILE" ] && continue
        
        if [ "$ONLY_TODAY" == "true" ]; then
            [ -z "$(find "$FILE" -newermt "$DATE_TODAY" -print -quit 2>/dev/null)" ] && continue
        fi

        OUTPUT+="\n\n--- File: $FILE ---\n"
        OUTPUT+="$(<"$FILE" tr -d '\0')\n"
    done < "$FILE_LIST"

elif [ "$MODE" == "directory" ]; then
    TARGET_DIR="${INPUT_PATH:-.}"
    [ ! -d "$TARGET_DIR" ] && echo "Error: $TARGET_DIR is not a directory." && exit 1

    while IFS= read -r -d '' FILE; do
        RELATIVE_PATH=${FILE#./}
        OUTPUT+="\n\n--- File: $RELATIVE_PATH ---\n"
        OUTPUT+="$(<"$FILE" tr -d '\0')\n"
    done < <(find_code_files "$TARGET_DIR" "$ONLY_TODAY")
fi

# 3. Output
if [ -z "$OUTPUT" ]; then
    echo "No files found matching the criteria."
else
    copy_to_clipboard "$OUTPUT"
    echo "Codebase content copied to clipboard (Today filter: $ONLY_TODAY)."
fi