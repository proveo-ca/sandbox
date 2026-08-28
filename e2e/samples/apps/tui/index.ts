import { formatSystemStatus, getWelcomeHeader } from "@polyglot/utils";

const ESC = "\u001b";
const CLEAR = "\u001b[2J\u001b[H";
const HIDE_CURSOR = "\u001b[?25l";
const SHOW_CURSOR = "\u001b[?25h";
const BLUE = "\u001b[34m";
const GREEN = "\u001b[32m";
const YELLOW = "\u001b[33m";
const BOLD = "\u001b[1m";
const RESET = "\u001b[0m";

const options = [
  "Say Hello World",
  "Check Go API Connection",
  "Trigger Rust Integration Harness",
  "Exit"
];

let selectedIndex = 0;
let lastMessage = "";

function render() {
  process.stdout.write(CLEAR);
  process.stdout.write(HIDE_CURSOR);
  process.stdout.write(`${BOLD}${BLUE}${getWelcomeHeader()}${RESET}\n`);
  process.stdout.write(`Use Arrow keys or 'j'/'k' to select, Enter to choose, 'q' to quit.\n\n`);

  for (let i = 0; i < options.length; i++) {
    if (i === selectedIndex) {
      process.stdout.write(`  ${GREEN}${BOLD}> ${options[i]}${RESET}\n`);
    } else {
      process.stdout.write(`    ${options[i]}\n`);
    }
  }

  process.stdout.write(`\n`);
  if (lastMessage) {
    process.stdout.write(`${YELLOW}${formatSystemStatus(lastMessage)}${RESET}\n`);
  } else {
    process.stdout.write(`System Status: Ready.\n`);
  }
}

async function handleAction(index: number) {
  switch (index) {
    case 0:
      lastMessage = "Hello World from Bun TUI!";
      break;
    case 1:
      lastMessage = "Checking Go API (localhost:8080)...";
      render();
      try {
        const res = await fetch("http://localhost:8080/");
        if (res.ok) {
          const data = await res.json() as { message: string };
          lastMessage = `Go API Connected: "${data.message}"`;
        } else {
          lastMessage = `Go API error status: ${res.status}`;
        }
      } catch (err: any) {
        lastMessage = `Go API Unreachable: ${err.message}`;
      }
      break;
    case 2:
      lastMessage = "Triggering Rust Integration Harness...";
      render();
      try {
        const proc = Bun.spawn(["cargo", "run", "--manifest-path", "../harness/Cargo.toml"]);
        const text = await new Response(proc.stdout).text();
        lastMessage = `Rust Harness output: ${text.trim().split("\n").pop()}`;
      } catch (err: any) {
        lastMessage = `Rust Harness failed: ${err.message}`;
      }
      break;
    case 3:
      exitTUI();
      break;
  }
  render();
}

function exitTUI() {
  process.stdout.write(SHOW_CURSOR);
  process.stdout.write(CLEAR);
  process.stdout.write("Exiting TUI. Goodbye!\n");
  process.exit(0);
}

if (process.stdin.isTTY) {
  process.stdin.setRawMode(true);
}
process.stdin.resume();
process.stdin.setEncoding("utf8");

render();

process.stdin.on("data", async (key: string) => {
  if (key === "\u0003" || key === "q" || key === "Q") {
    exitTUI();
  }

  if (key === "\r" || key === "\n") {
    await handleAction(selectedIndex);
    return;
  }

  if (key === `${ESC}[A` || key === "k" || key === "K") {
    selectedIndex = (selectedIndex - 1 + options.length) % options.length;
    render();
  } else if (key === `${ESC}[B` || key === "j" || key === "J") {
    selectedIndex = (selectedIndex + 1) % options.length;
    render();
  }
});
