/**
 * Shared utility functions for the Polyglot Monorepo.
 */

export function formatSystemStatus(status: string): string {
  const timestamp = new Date().toLocaleTimeString();
  return `[${timestamp}] ${status}`;
}

export function getWelcomeHeader(): string {
  return "=== PROVEO POLYGLOT MONOREPO TUI ===";
}
