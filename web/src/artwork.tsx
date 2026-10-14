import { ArrowUpRight, ArrowRight, Code2 } from "lucide-react";
export function SiteArtwork({ kind }: { kind: "report" | "api" | "product" }) {
  if (kind === "report")
    return (
      <div className="art art-report" aria-hidden="true">
        <div className="art-top">
          <span>DOUYA / ENGINEERING</span>
          <span>VOL. 042</span>
        </div>
        <div className="report-title">
          BUILD.
          <br />
          <em>REFLECT.</em>
          <br />
          REPEAT.
        </div>
        <div className="report-stamp">
          WEEK
          <br />
          <strong>42</strong>
        </div>
        <div className="report-lines">
          <i />
          <i />
          <i />
          <i />
          <i />
          <i />
        </div>
        <div className="art-bottom">
          <span>每周一点，持续向前。</span>
          <ArrowUpRight size={16} />
        </div>
      </div>
    );
  if (kind === "api")
    return (
      <div className="art art-api" aria-hidden="true">
        <div className="art-top">
          <span>
            <i /> SYSTEM OBSERVABILITY
          </span>
          <span>STATIC</span>
        </div>
        <div className="api-title">
          Every request.
          <br />
          <span>Understood.</span>
        </div>
        <div className="api-stats">
          <span>
            HTML<small>ready for the web</small>
          </span>
          <span>
            CSS<small>room for ideas</small>
          </span>
        </div>
        <svg className="wave" viewBox="0 0 400 100" preserveAspectRatio="none">
          <defs>
            <linearGradient id="wave-fill" x1="0" y1="0" x2="0" y2="1">
              <stop stopColor="#94b7fd" stopOpacity=".35" />
              <stop offset="1" stopColor="#94b7fd" stopOpacity="0" />
            </linearGradient>
          </defs>
          <path
            d="M0 90C20 90 21 75 39 75S62 90 77 65 100 77 119 50 148 77 165 48 185 57 202 35 223 66 239 29 260 60 281 26 300 37 322 13 348 37 370 12 383 16 400 2V100H0Z"
            fill="url(#wave-fill)"
          />
          <path
            d="M0 90C20 90 21 75 39 75S62 90 77 65 100 77 119 50 148 77 165 48 185 57 202 35 223 66 239 29 260 60 281 26 300 37 322 13 348 37 370 12 383 16 400 2"
            fill="none"
            stroke="#a3c0ff"
            strokeWidth="2"
          />
        </svg>
      </div>
    );
  return (
    <div className="art art-product" aria-hidden="true">
      <div className="art-top">
        <span>studio / a little space</span>
        <span>↗</span>
      </div>
      <div className="product-title">
        Make room
        <br />
        for <em>ideas.</em>
      </div>
      <div className="sculpture">
        <div className="shape one" />
        <div className="shape two" />
        <div className="shape three" />
      </div>
      <div className="product-bottom">
        A NEW WAY TO CREATE
        <span>
          Explore the demo <ArrowRight size={12} />
        </span>
      </div>
    </div>
  );
}
export function FileStack() {
  return (
    <div className="file-stack" aria-hidden="true">
      <div className="file-paper back">
        <span>ZIP</span>
        <i />
        <i />
      </div>
      <div className="file-paper middle">
        <span>CSS</span>
        <i />
        <i />
      </div>
      <div className="file-paper front">
        <Code2 size={29} />
        <span>index.html</span>
        <div className="paper-rule" />
      </div>
      <div className="stack-arrow">
        <ArrowUpRight size={21} />
      </div>
    </div>
  );
}
