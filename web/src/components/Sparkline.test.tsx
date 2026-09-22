import { describe, expect, it } from 'vitest';
import { render, screen } from '@testing-library/react';
import { Sparkline } from './Sparkline';

describe('Sparkline', () => {
  it('draws one point per sample', () => {
    const { container } = render(<Sparkline values={[1, 4, 2]} label="Outbox depth" />);

    const line = container.querySelector('polyline');
    expect(line).not.toBeNull();
    expect(line?.getAttribute('points')?.trim().split(/\s+/)).toHaveLength(3);
  });

  it('states the latest value and the peak, because a shape alone is not readable', () => {
    render(<Sparkline values={[1, 9, 3]} label="Outbox depth" />);

    const chart = screen.getByRole('img');
    expect(chart).toHaveAccessibleName('Outbox depth, now 3, peak 9, over 3 samples');
  });

  it('draws a flat series without dividing by a zero range', () => {
    const { container } = render(<Sparkline values={[7, 7, 7]} label="Outbox depth" />);

    const points = container.querySelector('polyline')?.getAttribute('points') ?? '';
    expect(points).not.toMatch(/NaN/);
    // A flat series sits on one row, and the row is the middle rather than the
    // top or the bottom, so nothing reads as a peak or as an empty chart.
    const rows = new Set(
      points
        .trim()
        .split(/\s+/)
        .map((point) => point.split(',')[1]),
    );
    expect(rows.size).toBe(1);
  });

  it('renders nothing before there are two samples to join', () => {
    const { container } = render(<Sparkline values={[3]} label="Outbox depth" />);

    expect(container.querySelector('polyline')).toBeNull();
    expect(screen.queryByRole('img')).not.toBeInTheDocument();
  });
});
