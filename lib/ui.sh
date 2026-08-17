#!/usr/bin/env bash
# SPEC: _spec/cmd/proveo/provision-and-targets.puml

if [[ -t 1 && -z "${NO_COLOR:-}" ]]; then
  BOLD=$'\033[1m'
  CYAN=$'\033[36m'
  GREEN=$'\033[32m'
  RESET=$'\033[0m'
else
  BOLD=""
  CYAN=""
  GREEN=""
  RESET=""
fi

tui_select() {
  local -a options=("$@")
  if [[ ${#options[@]} -eq 0 ]]; then
    return 0
  fi

  local -a options_lower=()
  local opt
  for opt in "${options[@]}"; do
    local opt_low
    opt_low=$(echo "$opt" | tr '[:upper:]' '[:lower:]')
    options_lower+=("$opt_low")
  done

  local idx=0
  local key
  local esc=$'\033'
  local filter_text=""

  printf '\033[?25l' >&2

  while true; do
    # Determine which options match the current filter
    local -a filtered_indices=()
    local filter_lower=""
    if [[ -n "$filter_text" ]]; then
      filter_lower=$(echo "$filter_text" | tr '[:upper:]' '[:lower:]')
    fi

    for i in "${!options[@]}"; do
      local opt_lower="${options_lower[$i]}"
      if [[ -z "$filter_text" ]] || [[ "$opt_lower" == *"$filter_lower"* ]]; then
        filtered_indices+=("$i")
      fi
    done

    local count=${#filtered_indices[@]}
    if [[ $idx -ge $count ]]; then
      idx=0
    fi
    if [[ $idx -lt 0 && $count -gt 0 ]]; then
      idx=$((count - 1))
    fi

    # Render options list
    local printed_lines=0
    if [[ -n "$filter_text" ]]; then
      printf '  %sFilter: %s%s\n' "$CYAN" "$filter_text" "$RESET" >&2
      printed_lines=$((printed_lines + 1))
    fi

    if [[ ${#filtered_indices[@]} -gt 0 ]]; then
      for i in "${!filtered_indices[@]}"; do
        local real_idx="${filtered_indices[$i]}"
        if [[ $i -eq $idx ]]; then
          printf '  %s%s>%s %s%s\n' "$GREEN" "$BOLD" "$RESET" "${options[$real_idx]}" "$RESET" >&2
        else
          printf '    %s\n' "${options[$real_idx]}" >&2
        fi
        printed_lines=$((printed_lines + 1))
      done
    fi

    # Read key
    IFS= read -rsn1 key
    if [[ $key == "$esc" ]]; then
      IFS= read -rsn2 -t 1 key || true
      case "$key" in
        '[A'|'OA') idx=$(( (idx - 1 + count) % count )) ;;
        '[B'|'OB') idx=$(( (idx + 1) % count )) ;;
      esac
    elif [[ -z "$key" || $key == $'\x0a' || $key == $'\x0d' ]]; then
      if [[ $count -gt 0 ]]; then
        idx="${filtered_indices[$idx]}"
        # Clear rendered TUI lines before breaking
        if [[ $printed_lines -gt 0 ]]; then
          printf '\033[%dA' "$printed_lines" >&2
          printf '\033[J' >&2
        fi
        break
      else
        printf '\a' >&2
      fi
    elif [[ $key == 'k' || $key == 'K' ]]; then
      if [[ $count -gt 0 ]]; then
        idx=$(( (idx - 1 + count) % count ))
      fi
    elif [[ $key == 'j' || $key == 'J' ]]; then
      if [[ $count -gt 0 ]]; then
        idx=$(( (idx + 1) % count ))
      fi
    elif [[ $key == 'q' || $key == 'Q' ]]; then
      if [[ $printed_lines -gt 0 ]]; then
        printf '\033[%dA' "$printed_lines" >&2
        printf '\033[J' >&2
      fi
      printf '\033[?25h' >&2
      return 255
    elif [[ $key == $'\177' || $key == $'\x08' ]]; then
      # Backspace
      if [[ ${#filter_text} -gt 0 ]]; then
        filter_text="${filter_text%?}"
        idx=0
      fi
    elif [[ "$key" =~ [a-zA-Z0-9[:space:]_./-] ]]; then
      # Test if appending matches anything
      local new_filter="${filter_text}${key}"
      local nf_lower
      nf_lower=$(echo "$new_filter" | tr '[:upper:]' '[:lower:]')
      local -a test_indices=()
      for o in "${options_lower[@]}"; do
        if [[ "$o" == *"$nf_lower"* ]]; then
          test_indices+=(1)
        fi
      done
      if [[ ${#test_indices[@]} -gt 0 ]]; then
        filter_text="$new_filter"
        idx=0
      else
        # Invalid input / no matching options triggers warning beep (retry loop)
        printf '\a' >&2
      fi
    else
      # Invalid input triggers warning beep (retry loop)
      printf '\a' >&2
    fi

    # Clear rendered TUI lines for redraw
    if [[ $printed_lines -gt 0 ]]; then
      printf '\033[%dA' "$printed_lines" >&2
      printf '\033[J' >&2
    fi
  done

  printf '\033[?25h' >&2
  return "$idx"
}
