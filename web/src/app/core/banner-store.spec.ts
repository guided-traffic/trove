import { TestBed } from '@angular/core/testing';
import { BannerStore } from './banner-store';

describe('BannerStore', () => {
  it('R-UI-002 holds no banner by default and trims the text', () => {
    const store = TestBed.inject(BannerStore);
    expect(store.text()).toBeNull();
    store.set('  Planned downtime  ');
    expect(store.text()).toBe('Planned downtime');
  });

  it('R-UI-002 treats blank text and null as no banner', () => {
    const store = TestBed.inject(BannerStore);
    store.set('x');
    store.set('   ');
    expect(store.text()).toBeNull();
    store.set('y');
    store.set(null);
    expect(store.text()).toBeNull();
  });
});
