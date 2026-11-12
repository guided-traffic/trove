import { DatePipe } from '@angular/common';
import { Component, computed, input } from '@angular/core';

/**
 * How long ago a time was, in cogpit's short form: "just now", "3 min ago", "1 h ago", "4 d ago" (Q-UI-378). A time ahead of `now`
 * (a clock that runs ahead) reads "just now".
 */
export function timeAgo(iso: string, now: number = Date.now()): string {
  const minutes = Math.floor((now - Date.parse(iso)) / 60_000);
  if (!(minutes >= 1)) return $localize`:@@time.ago.now:just now`;
  if (minutes < 60) return $localize`:@@time.ago.minutes:${minutes}:count: min ago`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return $localize`:@@time.ago.hours:${hours}:count: h ago`;
  return $localize`:@@time.ago.days:${Math.floor(hours / 24)}:count: d ago`;
}

/**
 * A time as one relative time with the absolute time in brackets, "3 min ago (Nov 12, 2026, 2:22:37 PM)" (Q-UI-378): the one form
 * of the grade card's facts (R-UI-093), and of any place that shows a time with both.
 */
@Component({
  selector: 'trove-time-ago',
  imports: [DatePipe],
  template: `{{ relative() }} (<time [attr.datetime]="at()">{{ at() | date: 'medium' }}</time>)`,
})
export class TimeAgo {
  /** The time, RFC 3339. */
  readonly at = input.required<string>();
  /** The time it is now, in milliseconds; a test sets it, the page leaves it to the clock. */
  readonly now = input<number | null>(null);

  protected readonly relative = computed(() => timeAgo(this.at(), this.now() ?? Date.now()));
}
