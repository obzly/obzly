import { WEBSITE_URL } from '../../../constants';

export function AuthNavbarComponent() {
  return (
    <div className="m-3 flex h-[65px] items-center justify-center p-3 sm:justify-start">
      <div className="flex items-center gap-3 hover:opacity-80">
        <a href={WEBSITE_URL} target="_blank" rel="noreferrer">
          <img className="h-[35px] w-[35px]" src="/logo.svg" />
        </a>

        <div className="text-xl font-bold">
          <a
            href={WEBSITE_URL}
            className="!text-black dark:!text-white"
            target="_blank"
            rel="noreferrer"
          >
            Obzly
          </a>
        </div>
      </div>
    </div>
  );
}
