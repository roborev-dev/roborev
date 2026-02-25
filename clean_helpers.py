import re

with open("cmd/roborev/tui_filter_test_helpers.go", "r") as f:
    content = f.read()

unused_funcs = [
    "withFilterSearch",
    "withHideAddressed",
    "withLoadingJobs",
    "withLoadingMore",
    "withFetchSeq",
    "withWidth",
    "withHeight",
    "assertNodeState",
    "assertFlatListLen"
]

for func in unused_funcs:
    content = re.sub(r'func ' + func + r'\(.*?\).*?\{.*?\}
', '', content, flags=re.DOTALL)

# Delete nodeState struct
content = re.sub(r'type nodeState struct \{.*?\}
', '', content, flags=re.DOTALL)

with open("cmd/roborev/tui_filter_test_helpers.go", "w") as f:
    f.write(content)
