import os
import re

def split_go_file(filename):
    with open(filename, "r") as f:
        content = f.read()

    # Extract imports
    imports_match = re.search(r'package main

import \((.*?)\)
', content, re.DOTALL)
    imports = imports_match.group(1) if imports_match else ""

    # Split into chunks (top-level declarations)
    # A simple way to split top level functions/vars is to split by "
func " and "
var "
    # We will use an AST-like regex or brace counting, but since Go format puts "}" at the beginning of the line
    # we can split by "
}
" followed by "
func "
    
    chunks = re.split(r'
(?=func )', content)
    
    header = chunks[0]
    functions = chunks[1:]

    files = {
        "tui_filter_tree_test.go": [],
        "tui_filter_search_test.go": [],
        "tui_filter_stack_test.go": [],
        "tui_filter_queue_test.go": [],
        "tui_filter_test.go": [] # Remainder
    }
    
    for func_text in functions:
        func_text = "func " + func_text
        name_match = re.match(r'func ([A-Za-z0-9_]+)\(', func_text)
        if not name_match:
            files["tui_filter_test.go"].append(func_text)
            continue
        
        name = name_match.group(1)
        
        if "Tree" in name or "Branch" in name or "LazyLoad" in name or "Expand" in name or "Collapse" in name or "RootPath" in name or "count" in name:
            if "BranchFilter" in name:
                files["tui_filter_queue_test.go"].append(func_text)
            else:
                files["tui_filter_tree_test.go"].append(func_text)
        elif "Search" in name or "Typing" in name or "Backspace" in name or "HAndL" in name:
            files["tui_filter_search_test.go"].append(func_text)
        elif "Stack" in name or "Escape" in name or "Clear" in name or "Pop" in name or "Locked" in name or "RemoveFilter" in name:
            files["tui_filter_stack_test.go"].append(func_text)
        elif "Queue" in name or "ZeroVisible" in name or "Refresh" in name or "Nav" in name or "Status" in name or "Batches" in name or "Cwd" in name:
            files["tui_filter_queue_test.go"].append(func_text)
        else:
            files["tui_filter_test.go"].append(func_text)

    # Write files
    for fname, funcs in files.items():
        if not funcs:
            continue
        # We need imports. tui_filter_test.go retains the original header.
        if fname == "tui_filter_test.go":
            new_content = header + "
" + "
".join(funcs)
        else:
            new_content = "package main

import (
" + imports + "
)

" + "
".join(funcs)
        
        with open("cmd/roborev/" + fname, "w") as f:
            f.write(new_content)
            
split_go_file("cmd/roborev/tui_filter_test.go")
