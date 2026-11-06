import { useEffect, useState } from 'react';

/** Screen height via visualViewport: 100vh/100dvh misbehave on iOS with the keyboard open. */
export function useScreenHeight(): number {
  const [screenHeight, setScreenHeight] = useState<number>(900);

  useEffect(() => {
    const updateHeight = () => {
      const height = window.visualViewport ? window.visualViewport.height : window.innerHeight;
      setScreenHeight(height);
    };

    updateHeight();
    window.addEventListener('resize', updateHeight);

    if (window.visualViewport) {
      window.visualViewport.addEventListener('resize', updateHeight);
    }

    return () => {
      window.removeEventListener('resize', updateHeight);
      if (window.visualViewport) {
        window.visualViewport.removeEventListener('resize', updateHeight);
      }
    };
  }, []);

  return screenHeight;
}
