import { UpOutlined } from '@ant-design/icons';
import { Button } from 'antd';
import React, { useCallback, useEffect, useRef, useState } from 'react';

interface Props {
  containerRef: React.RefObject<HTMLDivElement | null>;
  scrollThreshold?: number;
}

export const FloatingTopButtonComponent = ({
  containerRef,
  scrollThreshold = 200,
}: Props): React.JSX.Element | null => {
  const [showScrollToTop, setShowScrollToTop] = useState(false);
  const scrollTimeoutRef = useRef<number | null>(null);

  const handleScroll = useCallback(() => {
    if (!containerRef.current) {
      return;
    }

    const container = containerRef.current;
    const scrollTop = container.scrollTop;

    const shouldShowButton = scrollTop > scrollThreshold;

    setShowScrollToTop((prev) => {
      if (prev !== shouldShowButton) {
        return shouldShowButton;
      }
      return prev;
    });
  }, [containerRef, scrollThreshold]);

  const throttledHandleScroll = useCallback(() => {
    if (scrollTimeoutRef.current) {
      return;
    }

    scrollTimeoutRef.current = setTimeout(() => {
      handleScroll();
      scrollTimeoutRef.current = null;
    }, 16); // ~60fps
  }, [handleScroll]);

  const scrollToTop = useCallback(() => {
    if (containerRef.current) {
      containerRef.current.scrollTo({
        top: 0,
        behavior: 'smooth',
      });
    }
  }, [containerRef]);

  useEffect(() => {
    const container = containerRef.current;
    if (!container) {
      return;
    }

    handleScroll();

    container.addEventListener('scroll', throttledHandleScroll);
    return () => {
      container.removeEventListener('scroll', throttledHandleScroll);
      if (scrollTimeoutRef.current) {
        clearTimeout(scrollTimeoutRef.current);
        scrollTimeoutRef.current = null;
      }
    };
  }, [handleScroll, throttledHandleScroll]);

  if (!showScrollToTop) {
    return null;
  }

  return (
    <div
      className="fixed z-50"
      style={{
        top: '100px',
        right: '25px',
        // Below antd's Drawer/Modal z-index (1000) so open overlays stay on top.
        zIndex: 100,
      }}
    >
      <Button
        type="primary"
        className="border-emerald-600 bg-emerald-600 shadow-lg hover:border-emerald-700 hover:bg-emerald-700"
        shape="circle"
        icon={<UpOutlined />}
        size="large"
        onClick={scrollToTop}
      />
    </div>
  );
};
