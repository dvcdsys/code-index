import React, { useState } from "react";

interface Props {
  name: string;
  initialCount?: number;
}

type Click = (n: number) => void;

export const Counter: React.FC<Props> = ({ name, initialCount = 0 }) => {
  const [count, setCount] = useState<number>(initialCount);

  const handleClick: Click = (n) => setCount(count + n);

  return (
    <div className="counter">
      <h2>Hello, {name}!</h2>
      <p>Count: {count}</p>
      <button onClick={() => handleClick(1)}>+</button>
      <button onClick={() => handleClick(-1)}>-</button>
    </div>
  );
};

export function App(): JSX.Element {
  return <Counter name="world" initialCount={0} />;
}
